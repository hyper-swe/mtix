// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// LWW resolution per FR-18.11 / SYNC-DESIGN section 8.2 applies to
// these op_types only — they share the field-level conflict semantics
// where two concurrent events touching the same logical field need a
// deterministic winner. Other op_types have their own semantics
// (delete is monotonic; comments are append-only; deps are idempotent;
// claim and transition_status use most-recent-applied-wins).
//
// fieldKeyForLWW returns the (op_type-prefixed) field identifier used
// to scope LWW lookups, or "" if this op is not LWW-eligible.
func fieldKeyForLWW(e *model.SyncEvent) string {
	switch e.OpType {
	case model.OpUpdateField:
		var p model.UpdateFieldPayload
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			return ""
		}
		return "update_field:" + p.FieldName
	case model.OpSetAcceptance:
		return "set_acceptance:acceptance"
	case model.OpSetPrompt:
		return "set_prompt:prompt"
	default:
		return ""
	}
}

// lwwOutcome is the result of comparing an incoming event against
// the highest-lamport prior event for the same (node, field).
type lwwOutcome struct {
	HasPrior        bool   // true iff there's a prior event for the same field
	PriorEventID    string // empty when HasPrior is false
	IncomingWins    bool   // true iff the incoming event beats the prior on (lamport, ts, hash)
	FieldName       string // for the conflict log
}

// detectLWWOutcome looks up the highest-lamport prior event matching
// this event's (node_id, field_key) in sync_events and computes the
// LWW outcome. Returns HasPrior=false when no prior exists; in that
// case the caller proceeds with the apply unconditionally.
func detectLWWOutcome(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (lwwOutcome, error) {
	key := fieldKeyForLWW(e)
	if key == "" {
		return lwwOutcome{}, nil
	}
	fieldName := strings.TrimPrefix(strings.SplitN(key, ":", 2)[1], "")

	// Match prior events on the same (node, op_type, field). For
	// update_field we also need to filter by payload->>'field_name';
	// for set_acceptance / set_prompt the op_type alone is sufficient.
	var query string
	args := []any{e.NodeID, string(e.OpType), e.EventID}
	switch e.OpType {
	case model.OpUpdateField:
		query = `SELECT event_id, lamport_clock, wall_clock_ts, author_machine_hash
		         FROM sync_events
		         WHERE node_id = ? AND op_type = ? AND event_id <> ?
		           AND json_extract(payload, '$.field_name') = ?
		         ORDER BY lamport_clock DESC, wall_clock_ts DESC, author_machine_hash ASC
		         LIMIT 1`
		args = append(args, fieldName)
	default:
		query = `SELECT event_id, lamport_clock, wall_clock_ts, author_machine_hash
		         FROM sync_events
		         WHERE node_id = ? AND op_type = ? AND event_id <> ?
		         ORDER BY lamport_clock DESC, wall_clock_ts DESC, author_machine_hash ASC
		         LIMIT 1`
	}

	var (
		priorID   string
		priorLamp int64
		priorTS   int64
		priorHash string
	)
	err := tx.QueryRowContext(ctx, query, args...).Scan(&priorID, &priorLamp, &priorTS, &priorHash)
	if errors.Is(err, sql.ErrNoRows) {
		return lwwOutcome{FieldName: fieldName}, nil
	}
	if err != nil {
		return lwwOutcome{}, fmt.Errorf("LWW lookup: %w", err)
	}
	return lwwOutcome{
		HasPrior:     true,
		PriorEventID: priorID,
		IncomingWins: incomingBeats(e, priorLamp, priorTS, priorHash),
		FieldName:    fieldName,
	}, nil
}

// incomingBeats encodes the FR-18.11 / SYNC-DESIGN section 8.2 LWW
// total ordering: higher lamport wins; tie-break by higher
// wall_clock_ts; final tie-break by lower author_machine_hash
// (lex compare). Equal on all three is considered NOT a win for the
// incoming (the prior is already applied; no need to re-apply).
func incomingBeats(e *model.SyncEvent, priorLamp, priorTS int64, priorHash string) bool {
	if e.LamportClock != priorLamp {
		return e.LamportClock > priorLamp
	}
	if e.WallClockTS != priorTS {
		return e.WallClockTS > priorTS
	}
	return e.AuthorMachineHash < priorHash
}

// mirrorIncomingEvent records a pulled event into the local
// sync_events table with sync_status='applied'. ON CONFLICT DO NOTHING
// makes the call safe even when the same event was previously emitted
// locally (we'd then have it as 'pending' or 'pushed'; the mirror
// attempt is a no-op).
func mirrorIncomingEvent(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	vcJSON, err := json.Marshal(e.VectorClock)
	if err != nil {
		return fmt.Errorf("mirror %s: marshal VC: %w", e.EventID, err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO sync_events
		  (event_id, project_prefix, node_id, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash, sync_status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.EventID, e.ProjectPrefix, e.NodeID, string(e.OpType), string(e.Payload),
		e.WallClockTS, e.LamportClock, string(vcJSON),
		e.AuthorID, e.AuthorMachineHash, string(model.SyncStatusApplied),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// dispatchWithLWW runs the LWW resolution pipeline: detect outcome,
// either skip the apply (loser branch) or run dispatchApply (winner
// or no-prior branch), and record the conflict row when applicable.
//
// Extracted from IdempotentApply to keep cyclomatic complexity below
// the package's lint threshold.
func dispatchWithLWW(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	outcome, err := detectLWWOutcome(ctx, tx, event)
	if err != nil {
		return fmt.Errorf("apply %s: LWW: %w", event.EventID, err)
	}

	if outcome.HasPrior && !outcome.IncomingWins {
		if err := recordLocalConflict(ctx, tx,
			outcome.PriorEventID, event.EventID, event.NodeID, outcome.FieldName,
		); err != nil {
			return fmt.Errorf("apply %s: record loser conflict: %w", event.EventID, err)
		}
		return nil
	}

	if err := dispatchApply(ctx, tx, event); err != nil {
		return err
	}
	if outcome.HasPrior && outcome.IncomingWins {
		if err := recordLocalConflict(ctx, tx,
			event.EventID, outcome.PriorEventID, event.NodeID, outcome.FieldName,
		); err != nil {
			return fmt.Errorf("apply %s: record winner conflict: %w", event.EventID, err)
		}
	}
	return nil
}

// recordLocalConflict persists a row to the local sync_conflicts
// table for surfacing via mtix sync conflicts list (MTIX-15.7).
func recordLocalConflict(ctx context.Context, tx *sql.Tx, winnerID, loserID, nodeID, fieldName string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sync_conflicts
		  (event_id_winner, event_id_loser, node_id, field_name, resolution, resolved_at)
		VALUES (?, ?, ?, ?, 'lww', ?)`,
		winnerID, loserID, nodeID, nullableString(fieldName),
		time.Now().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// IdempotentApply applies a single sync event to the local nodes table
// per FR-18.9 / SYNC-DESIGN section 8. Symmetric to emitEvent: emit
// writes our own mutations into sync_events; apply consumes events
// pulled from the hub and replays them against the nodes table.
//
// MUST be called inside an open *sql.Tx (caller's responsibility, same
// as emitEvent). The caller is the apply loop in MTIX-15.7's mtix sync
// pull; until that lands, the only callers are the tests in this
// package.
//
// Behavior:
//   - Duplicate event_id: silent no-op (FR-18.9 idempotency).
//   - Validates the event before any mutation; invalid events surface
//     ErrInvalidInput.
//   - Dispatches on op_type to a per-op apply function.
//   - DOES NOT emit a sync_event (apply MUST NOT loop).
//   - Always advances local Lamport to max(local, event.lamport) and
//     merges event.author_id into the local vector clock.
//   - Records the event in applied_events on success.
func IdempotentApply(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	if event == nil {
		return fmt.Errorf("apply: event nil: %w", model.ErrInvalidInput)
	}
	if err := event.Validate(); err != nil {
		return fmt.Errorf("apply %s: %w", event.EventID, err)
	}

	already, err := isAppliedEvent(ctx, tx, event.EventID)
	if err != nil {
		return fmt.Errorf("apply %s: dedupe check: %w", event.EventID, err)
	}
	if already {
		return nil
	}

	// Mirror the event into the local sync_events log so subsequent
	// LWW lookups find it. A locally-emitted event already exists
	// (sync_status='pending' or 'pushed'); ON CONFLICT DO NOTHING
	// makes this a no-op for those.
	if mirrorErr := mirrorIncomingEvent(ctx, tx, event); mirrorErr != nil {
		return fmt.Errorf("apply %s: mirror: %w", event.EventID, mirrorErr)
	}

	if err := dispatchWithLWW(ctx, tx, event); err != nil {
		return err
	}

	if err := advanceLamport(ctx, tx, event.LamportClock); err != nil {
		return fmt.Errorf("apply %s: advance lamport: %w", event.EventID, err)
	}
	if err := mergeVectorClock(ctx, tx, event.AuthorID, event.VectorClock); err != nil {
		return fmt.Errorf("apply %s: merge VC: %w", event.EventID, err)
	}
	if err := recordApplied(ctx, tx, event); err != nil {
		return fmt.Errorf("apply %s: record applied: %w", event.EventID, err)
	}
	return nil
}

// isAppliedEvent returns true iff event_id already exists in the
// applied_events table (FR-18.9 dedupe key).
func isAppliedEvent(ctx context.Context, tx *sql.Tx, eventID string) (bool, error) {
	var exists int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM applied_events WHERE event_id = ?`, eventID,
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// recordApplied marks the event as applied so a re-pull-and-replay is
// a no-op. INSERT OR IGNORE handles the race where two concurrent tx
// race to apply the same event (only one wins; the other is a no-op).
func recordApplied(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO applied_events (event_id, applied_at, applied_by_lamport)
		 VALUES (?, ?, ?)`,
		event.EventID, time.Now().UTC().Format(time.RFC3339Nano), event.LamportClock,
	)
	return err
}

// advanceLamport sets meta.sync.lamport to max(current, observed).
// Lamport never goes backwards so a late-arriving event with a low
// clock cannot rewind us.
func advanceLamport(ctx context.Context, tx *sql.Tx, observed int64) error {
	var raw string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.lamport'`,
	).Scan(&raw)
	if err != nil {
		return err
	}
	current, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("parse current lamport %q: %w", raw, err)
	}
	if observed <= current {
		return nil
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.lamport'`,
		strconv.FormatInt(observed, 10),
	)
	return err
}

// mergeVectorClock merges the observed vector clock into the local one
// using per-key max. The author_id is also bumped to ensure our local
// view records that we observed this author at the new timestamp.
func mergeVectorClock(ctx context.Context, tx *sql.Tx, authorID string, observed model.VectorClock) error {
	var raw string
	err := tx.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.vector_clock'`,
	).Scan(&raw)
	if err != nil {
		return err
	}
	local := model.VectorClock{}
	if raw != "" && raw != "{}" && raw != "null" {
		if parseErr := json.Unmarshal([]byte(raw), &local); parseErr != nil {
			return fmt.Errorf("parse local VC %q: %w", raw, parseErr)
		}
	}
	merged := local.Merge(observed)
	// Ensure we record the author_id even if observed didn't include
	// itself (defensive — emit-time should have included it).
	if _, ok := merged[authorID]; !ok {
		merged[authorID] = 0
	}
	encoded, err := json.Marshal(merged)
	if err != nil {
		return fmt.Errorf("encode merged VC: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.vector_clock'`,
		string(encoded),
	)
	return err
}

// dispatchApply routes the event to the per-op_type handler.
func dispatchApply(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	switch e.OpType {
	case model.OpCreateNode:
		return applyCreateNode(ctx, tx, e)
	case model.OpUpdateField:
		return applyUpdateField(ctx, tx, e)
	case model.OpTransitionStatus:
		return applyTransitionStatus(ctx, tx, e)
	case model.OpClaim:
		return applyClaim(ctx, tx, e)
	case model.OpUnclaim:
		return applyUnclaim(ctx, tx, e)
	case model.OpDefer:
		return applyDefer(ctx, tx, e)
	case model.OpComment:
		return applyComment(ctx, tx, e)
	case model.OpLinkDep:
		return applyLinkDep(ctx, tx, e)
	case model.OpUnlinkDep:
		return applyUnlinkDep(ctx, tx, e)
	case model.OpDelete:
		return applyDelete(ctx, tx, e)
	case model.OpSetAcceptance:
		return applySetAcceptance(ctx, tx, e)
	case model.OpSetPrompt:
		return applySetPrompt(ctx, tx, e)
	default:
		return fmt.Errorf("unknown op_type %q: %w", e.OpType, model.ErrInvalidInput)
	}
}

// --- per-op_type apply functions ---

func applyCreateNode(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.CreateNodePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply create_node %s: decode payload: %w", e.EventID, err)
	}
	depth := computeDepth(p.ParentID)
	canonical := model.NodeTypeForDepth(depth) // FR-18.10 enforcement
	now := time.Now().UTC().Format(time.RFC3339)

	labelsJSON := "[]"
	if len(p.Labels) > 0 {
		b, err := json.Marshal(p.Labels)
		if err != nil {
			return fmt.Errorf("marshal labels: %w", err)
		}
		labelsJSON = string(b)
	}

	_, err := tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO nodes
		  (id, parent_id, depth, seq, project,
		   title, description, prompt, acceptance,
		   node_type, priority, labels,
		   status, progress,
		   assignee, creator,
		   created_at, updated_at,
		   weight,
		   activity, annotations)
		VALUES (?, ?, ?, ?, ?,
		        ?, ?, ?, ?,
		        ?, ?, ?,
		        ?, ?,
		        ?, ?,
		        ?, ?,
		        ?,
		        '[]', '[]')`,
		e.NodeID, nullableString(p.ParentID), depth, deriveSeq(e.NodeID), e.ProjectPrefix,
		p.Title, nullableString(p.Description), nullableString(p.Prompt), nullableString(p.Acceptance),
		string(canonical), int(p.Priority), labelsJSON,
		string(model.StatusOpen), 0.0,
		nullableString(p.Assignee), nullableString(p.Creator),
		now, now,
		1.0,
	)
	return err
}

// computeDepth returns the depth of a node given its parent ID. Depth 0
// for root nodes (no parent). For nested nodes we count the dots in
// the dot-notation tail past the project-dash separator.
//
// MTIX-1     -> depth 0
// MTIX-1.2   -> depth 1
// MTIX-1.2.3 -> depth 2
func computeDepth(parentID string) int {
	if parentID == "" {
		return 0
	}
	depth := 1
	for i := 0; i < len(parentID); i++ {
		if parentID[i] == '.' {
			depth++
		}
	}
	return depth
}

// deriveSeq pulls the trailing numeric segment out of a dot-notation
// node ID for the seq column. Best-effort; falls back to 0 if the ID
// shape doesn't fit. The seq column is informational; correctness
// doesn't depend on it.
func deriveSeq(nodeID string) int {
	last := -1
	for i := len(nodeID) - 1; i >= 0; i-- {
		if nodeID[i] == '.' || nodeID[i] == '-' {
			last = i
			break
		}
	}
	if last < 0 || last == len(nodeID)-1 {
		return 0
	}
	n, _ := strconv.Atoi(nodeID[last+1:])
	return n
}

// allowedUpdateFields is the column whitelist for applyUpdateField.
// Anything not in this map is rejected to prevent the apply path from
// becoming an arbitrary-SQL injection vector via a hostile payload.
var allowedUpdateFields = map[string]bool{
	"title":       true,
	"description": true,
	"prompt":      true,
	"acceptance":  true,
	"status":      true,
	"priority":    true,
	"labels":      true,
	"assignee":    true,
	"agent_state": true,
}

func applyUpdateField(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.UpdateFieldPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply update_field %s: decode payload: %w", e.EventID, err)
	}
	if !allowedUpdateFields[p.FieldName] {
		return fmt.Errorf("apply update_field %s: field %q not in whitelist: %w",
			e.EventID, p.FieldName, model.ErrInvalidInput)
	}
	if err := requireNodeExists(ctx, tx, e.NodeID); err != nil {
		return err
	}
	value, err := decodeNewValueForColumn(p.FieldName, p.NewValue)
	if err != nil {
		return fmt.Errorf("apply update_field %s: decode value: %w", e.EventID, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// SQL is constructed from a whitelisted column name; the value is
	// always a bound parameter.
	stmt := "UPDATE nodes SET " + p.FieldName + " = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL"
	_, err = tx.ExecContext(ctx, stmt, value, now, e.NodeID)
	return err
}

// decodeNewValueForColumn unmarshals the JSON-encoded new value into
// a Go value suitable for the target SQL column type. Whitelisted
// columns dictate the expected type.
func decodeNewValueForColumn(field string, raw json.RawMessage) (any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	switch field {
	case "priority":
		var v int
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return v, nil
	case "labels":
		// Store as JSON string in the labels column.
		return string(raw), nil
	default:
		var v string
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, err
		}
		return v, nil
	}
}

func applyTransitionStatus(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.TransitionStatusPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply transition_status %s: decode payload: %w", e.EventID, err)
	}
	if err := requireNodeExists(ctx, tx, e.NodeID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	closedAt := sql.NullString{}
	if p.To.IsTerminal() {
		closedAt = sql.NullString{String: now, Valid: true}
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, closed_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		string(p.To), closedAt, now, e.NodeID,
	)
	return err
}

func applyClaim(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.ClaimPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply claim %s: decode payload: %w", e.EventID, err)
	}
	if err := requireNodeExists(ctx, tx, e.NodeID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, assignee = ?, agent_state = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		string(model.StatusInProgress), p.AgentID, string(model.AgentStateWorking), now, e.NodeID,
	)
	return err
}

func applyUnclaim(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	if err := requireNodeExists(ctx, tx, e.NodeID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, assignee = NULL, agent_state = NULL, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		string(model.StatusOpen), now, e.NodeID,
	)
	return err
}

func applyDefer(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.DeferPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply defer %s: decode payload: %w", e.EventID, err)
	}
	if err := requireNodeExists(ctx, tx, e.NodeID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	deferUntil := sql.NullString{}
	if p.Until != nil {
		deferUntil = sql.NullString{String: p.Until.UTC().Format(time.RFC3339), Valid: true}
	}
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, defer_until = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		string(model.StatusDeferred), deferUntil, now, e.NodeID,
	)
	return err
}

func applyComment(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.CommentPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply comment %s: decode payload: %w", e.EventID, err)
	}
	if err := requireNodeExists(ctx, tx, e.NodeID); err != nil {
		return err
	}
	// Read existing annotations, append the new comment, write back.
	var raw sql.NullString
	if err := tx.QueryRowContext(ctx,
		`SELECT annotations FROM nodes WHERE id = ? AND deleted_at IS NULL`, e.NodeID,
	).Scan(&raw); err != nil {
		return err
	}
	annotations := []model.Annotation{}
	if raw.Valid && raw.String != "" {
		if err := json.Unmarshal([]byte(raw.String), &annotations); err != nil {
			return fmt.Errorf("decode annotations: %w", err)
		}
	}
	// Use the event's wall_clock_ts so two replicas applying the same
	// event in different orders produce byte-identical annotation
	// rows. Apply-time wall clock would diverge across replicas.
	annotations = append(annotations, model.Annotation{
		ID:        e.EventID,
		Author:    p.AuthorID,
		Text:      p.Body,
		CreatedAt: time.UnixMilli(e.WallClockTS).UTC(),
	})
	// Sort by (CreatedAt, ID) so the on-disk list order is independent
	// of apply order. Two replicas converging on the same set of
	// comments produce byte-identical annotations columns.
	sort.SliceStable(annotations, func(i, j int) bool {
		if annotations[i].CreatedAt.Equal(annotations[j].CreatedAt) {
			return annotations[i].ID < annotations[j].ID
		}
		return annotations[i].CreatedAt.Before(annotations[j].CreatedAt)
	})
	encoded, err := json.Marshal(annotations)
	if err != nil {
		return fmt.Errorf("encode annotations: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET annotations = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		string(encoded), time.UnixMilli(e.WallClockTS).UTC().Format(time.RFC3339), e.NodeID,
	)
	return err
}

func applyLinkDep(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.LinkDepPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply link_dep %s: decode payload: %w", e.EventID, err)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO dependencies (from_id, to_id, dep_type, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		e.NodeID, p.DependsOnNodeID, p.DepType, now, e.AuthorID,
	)
	return err
}

func applyUnlinkDep(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.UnlinkDepPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply unlink_dep %s: decode payload: %w", e.EventID, err)
	}
	depType := p.DepType
	if depType == "" {
		depType = string(model.DepTypeBlocks)
	}
	_, err := tx.ExecContext(ctx,
		`DELETE FROM dependencies WHERE from_id = ? AND to_id = ? AND dep_type = ?`,
		e.NodeID, p.DependsOnNodeID, depType,
	)
	return err
}

// applyDelete is a tombstone. SYNC-DESIGN section 8.3: delete on a
// non-existent node is a no-op (no phantom tombstone). Existing nodes
// get deleted_at set; this does NOT cascade — any descendants must
// have their own delete events.
func applyDelete(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET deleted_at = ?, deleted_by = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		now, e.AuthorID, now, e.NodeID,
	)
	return err
}

func applySetAcceptance(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.SetAcceptancePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply set_acceptance %s: decode payload: %w", e.EventID, err)
	}
	if err := requireNodeExists(ctx, tx, e.NodeID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET acceptance = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		p.AcceptanceText, now, e.NodeID,
	)
	return err
}

func applySetPrompt(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.SetPromptPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply set_prompt %s: decode payload: %w", e.EventID, err)
	}
	if err := requireNodeExists(ctx, tx, e.NodeID); err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET prompt = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		p.PromptText, now, e.NodeID,
	)
	return err
}

// requireNodeExists returns model.ErrNotFound (wrapped) if the node
// does not exist or is soft-deleted. Apply functions that target an
// existing row call this before mutating.
func requireNodeExists(ctx context.Context, tx *sql.Tx, nodeID string) error {
	var n int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM nodes WHERE id = ? AND deleted_at IS NULL`, nodeID,
	).Scan(&n)
	if err == sql.ErrNoRows {
		return fmt.Errorf("node %s: %w", nodeID, model.ErrNotFound)
	}
	return err
}
