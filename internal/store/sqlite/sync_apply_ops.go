// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// dispatchApply routes the event to the per-op_type handler.
func (a syncApplier) dispatchApply(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	switch e.OpType {
	case model.OpCreateNode:
		return a.applyCreateNode(ctx, tx, e)
	case model.OpUpdateField:
		return a.applyUpdateField(ctx, tx, e)
	case model.OpTransitionStatus:
		return a.applyTransitionStatus(ctx, tx, e)
	case model.OpClaim:
		return a.applyClaim(ctx, tx, e)
	case model.OpUnclaim:
		return a.applyUnclaim(ctx, tx, e)
	case model.OpDefer:
		return a.applyDefer(ctx, tx, e)
	case model.OpComment:
		return a.applyComment(ctx, tx, e)
	case model.OpLinkDep:
		return a.applyLinkDep(ctx, tx, e)
	case model.OpUnlinkDep:
		return a.applyUnlinkDep(ctx, tx, e)
	case model.OpDelete:
		return a.applyDelete(ctx, tx, e)
	case model.OpSetAcceptance:
		return a.applySetAcceptance(ctx, tx, e)
	case model.OpSetPrompt:
		return a.applySetPrompt(ctx, tx, e)
	default:
		return fmt.Errorf("unknown op_type %q: %w", e.OpType, model.ErrInvalidInput)
	}
}

// --- per-op_type apply functions ---

func (a syncApplier) applyCreateNode(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.CreateNodePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply create_node %s: decode payload: %w", format.EventID(e.EventID), err)
	}
	// The parent is named by number: refuse one a pending local task holds
	// (MTIX-95.37).
	if err := refusePendingNumber(ctx, tx, e.EventID, p.ParentID); err != nil {
		return err
	}
	now := a.clock().UTC().Format(time.RFC3339)

	labelsJSON := "[]"
	if len(p.Labels) > 0 {
		b, err := json.Marshal(p.Labels)
		if err != nil {
			return fmt.Errorf("marshal labels: %w", err)
		}
		labelsJSON = string(b)
	}

	// The node's durable uid is the create event's own id (ADR-003 §2
	// self-anchor). Dual-carry: prefer the carried UID, but tolerate a
	// uid-less old-CLI create by self-anchoring to the event id here too.
	uid := e.UID
	if uid == "" {
		uid = e.EventID
	}

	if err := refuseUIDHeldByOtherNode(ctx, tx, e, uid); err != nil {
		return err
	}

	// Idempotency no longer rides "OR IGNORE": a re-applied SAME create is
	// already short-circuited by the applied_events event_id check in
	// IdempotentApply BEFORE dispatch (FR-18.9). So the only thing that can
	// reach a row-level conflict here is a SECOND, DISTINCT create event
	// (different event_id/uid) that wants the same display_path — the
	// MTIX-28 split-brain. ADR-003 §6 resolves that by renumbering, which
	// is MTIX-30.7's job. Until then this keeps the documented residual
	// first-writer-wins behavior (e2e/sync_collision_test.go pins it), but
	// expresses it explicitly as a conflict on the id PK rather than as a
	// blanket OR IGNORE that also masked re-applies. A create whose uid
	// another local task holds is refused before the insert (MTIX-95.31.8), so
	// the pull quarantines it, visibly, instead of dropping it. It must NOT hard-error
	// (that would wedge the apply pipeline for the whole batch); the
	// surfacing/renumber is layered on top by 30.7.
	row := appliedCreateRow{payload: p, uid: uid, labels: labelsJSON, at: now}
	if err := insertAppliedCreate(ctx, tx, e, row); err != nil {
		return err
	}

	// MTIX-44: mirror the local CreateNode parent-progress rollup
	// (node_create.go, FR-5.7). A new leaf enters at progress 0.0 / weight
	// 1.0, which lowers the parent's weighted average; without recomputing,
	// a child created on another client and synced in leaves the local
	// parent's progress stale (and divergent from the emitting replica).
	// recalculateProgress tolerates a parent whose own create_node has not
	// yet applied (causal order).
	if p.ParentID != "" {
		if err := recalculateProgress(ctx, tx, p.ParentID); err != nil {
			return fmt.Errorf("apply create_node %s: recalc progress: %w", format.EventID(e.EventID), err)
		}
	}

	// MTIX-46: backfill content_hash for the synced-created node. The INSERT
	// above does not set content_hash (local CreateNode computes it separately),
	// so without this a synced-created node carries a NULL hash — breaking export
	// and import-merge identity checks (FR-7.8). Deterministic over content, so it
	// matches the emitting replica's hash.
	if err := recomputeContentHashRow(ctx, tx, e.NodeID); err != nil {
		return fmt.Errorf("apply create_node %s: %w", format.EventID(e.EventID), err)
	}
	return advanceAppliedSequence(ctx, tx, e, p.ParentID) // MTIX-95.38
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
	"issue_type":  true,
}

func (a syncApplier) applyUpdateField(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.UpdateFieldPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply update_field %s: decode payload: %w", format.EventID(e.EventID), err)
	}
	if !allowedUpdateFields[p.FieldName] {
		return fmt.Errorf("apply update_field %s: field %q not in whitelist: %w",
			format.EventID(e.EventID), p.FieldName, model.ErrInvalidInput)
	}
	id, err := resolveNodeRef(ctx, tx, e)
	if err != nil {
		return err
	}
	value, err := decodeNewValueForColumn(p.FieldName, p.NewValue)
	if err != nil {
		return fmt.Errorf("apply update_field %s: decode value: %w", format.EventID(e.EventID), err)
	}
	now := a.clock().UTC().Format(time.RFC3339)
	if p.FieldName == "issue_type" {
		return applyIssueTypeField(ctx, tx, id, value, now)
	}
	// SQL is constructed from a whitelisted column name; the value is
	// always a bound parameter.
	stmt := "UPDATE nodes SET " + p.FieldName + " = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL"
	if _, err = tx.ExecContext(ctx, stmt, value, now, id); err != nil {
		return err
	}
	// MTIX-46: maintain content_hash when a content-relevant field changes, so a
	// synced edit converges on the same hash the emitting replica stored. A
	// state-only field (status/priority/assignee/agent_state) leaves content
	// unchanged, so skip the recompute.
	if contentHashFields[p.FieldName] {
		return recomputeContentHashRow(ctx, tx, id)
	}
	return nil
}

// contentHashFields is the subset of allowedUpdateFields that feed
// ComputeContentHash (FR-3.7). A change to any of these must recompute
// content_hash on apply (MTIX-46).
var contentHashFields = map[string]bool{
	"title":       true,
	"description": true,
	"prompt":      true,
	"acceptance":  true,
	"labels":      true,
}

// decodeNewValueForColumn unmarshals the JSON-encoded new value into
// a Go value suitable for the target SQL column type. Whitelisted
// columns dictate the expected type.
func decodeNewValueForColumn(field string, raw json.RawMessage) (any, error) {
	if field == "issue_type" {
		return decodeIssueTypeUpdate(raw)
	}
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

// applyTransitionStatus applies a winning transition_status (MTIX-95.10). A
// malformed one changes nothing and does not fail (MTIX-95.27).
func (a syncApplier) applyTransitionStatus(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	in, ok := workflowInputForApply(e, a.clock().UTC())
	if !ok {
		return nil
	}
	id, err := applyWorkflowWinner(ctx, tx, e, in)
	if err != nil {
		return err
	}

	// MTIX-44: mirror the local transition path's derived-state recompute
	// (transition.go). A sync-applied resolution updates only its own status;
	// without this, a blocker resolved on another client and synced in leaves
	// local dependents sticky-blocked and parent progress stale. Runs in the
	// same tx so the applied status and the recompute are atomic.
	if parentID := model.ParseIDParent(id); parentID != "" {
		if err := recalculateProgress(ctx, tx, parentID); err != nil {
			return fmt.Errorf("apply transition_status %s: recalc progress: %w", format.EventID(e.EventID), err)
		}
	}
	if isResolvingStatus(in.to) {
		if err := unblockDependents(ctx, tx, a.store, id, e.AuthorID, a.clock); err != nil {
			return fmt.Errorf("apply transition_status %s: auto-unblock dependents: %w", format.EventID(e.EventID), err)
		}
	}
	return nil
}

// applyClaim applies a winning claim (MTIX-95.10). A claim whose payload
// cannot be decoded changes nothing and does not fail (MTIX-95.27).
func (a syncApplier) applyClaim(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	in, ok := workflowInputForApply(e, a.clock().UTC())
	if !ok {
		return nil
	}
	_, err := applyWorkflowWinner(ctx, tx, e, in)
	return err
}

// applyUnclaim applies a winning unclaim (MTIX-95.10). The workflow payload
// rule never rejects one (MTIX-95.27); the check keeps the four ops alike.
func (a syncApplier) applyUnclaim(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	in, ok := workflowInputForApply(e, a.clock().UTC())
	if !ok {
		return nil
	}
	_, err := applyWorkflowWinner(ctx, tx, e, in)
	return err
}

// applyDefer applies a winning defer (MTIX-95.10). A defer whose payload cannot
// be decoded, including an unparseable until, changes nothing and does not
// fail (MTIX-95.27); defer_until is stored in UTC (resolveWorkflowWrite).
func (a syncApplier) applyDefer(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	in, ok := workflowInputForApply(e, a.clock().UTC())
	if !ok {
		return nil
	}
	_, err := applyWorkflowWinner(ctx, tx, e, in)
	return err
}

func (a syncApplier) applyComment(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.CommentPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply comment %s: decode payload: %w", format.EventID(e.EventID), err)
	}
	id, err := resolveNodeRef(ctx, tx, e)
	if err != nil {
		return err
	}
	// Read existing annotations, append the new comment, write back.
	var raw sql.NullString
	if scanErr := tx.QueryRowContext(ctx,
		`SELECT annotations FROM nodes WHERE id = ? AND deleted_at IS NULL`, id,
	).Scan(&raw); scanErr != nil {
		return scanErr
	}
	annotations := []model.Annotation{}
	if raw.Valid && raw.String != "" {
		if decErr := json.Unmarshal([]byte(raw.String), &annotations); decErr != nil {
			return fmt.Errorf("decode annotations: %w", decErr)
		}
	}
	// The event's own time (eventTime, MTIX-95.26), not the apply time, so two
	// replicas applying the same events in any order write byte-identical rows.
	at := eventTime(e.WallClockTS, a.clock().UTC())
	annotations = append(annotations, model.Annotation{
		ID: e.EventID, Author: p.AuthorID, Text: p.Body, CreatedAt: at,
	})
	// Sort by (CreatedAt, ID) so the on-disk list order is independent
	// of apply order. Two replicas converging on the same set of
	// comments produce byte-identical annotations columns.
	annotations = model.SortAnnotations(annotations)
	encoded, err := json.Marshal(annotations)
	if err != nil {
		return fmt.Errorf("encode annotations: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`UPDATE nodes SET annotations = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		string(encoded), at.Format(time.RFC3339), id,
	)
	return err
}

func (a syncApplier) applyLinkDep(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.LinkDepPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply link_dep %s: decode payload: %w", format.EventID(e.EventID), err)
	}
	now := a.clock().UTC().Format(time.RFC3339)
	// Route the edge's source node through the uid-aware ref so a dep
	// survives a renumber of the from-node (ADR-003 §3). The dependencies
	// FK to nodes(id) is satisfied by the resolved current display path.
	fromID, err := fromIDForDepEdge(ctx, tx, e)
	if err != nil {
		return fmt.Errorf("apply link_dep %s: %w", format.EventID(e.EventID), err)
	}
	if err := refusePendingNumber(ctx, tx, e.EventID, p.DependsOnNodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT OR IGNORE INTO dependencies (from_id, to_id, dep_type, created_at, created_by)
		 VALUES (?, ?, ?, ?, ?)`,
		fromID, p.DependsOnNodeID, p.DepType, now, e.AuthorID,
	); err != nil {
		return err
	}

	// MTIX-44: mirror the local AddDependency derived-state recompute
	// (dependency.go). A synced `blocks` edge must auto-block its dependent,
	// exactly as the local path does via autoBlockNode; without this a
	// dependency created on another client and synced in leaves the local
	// dependent un-blocked. The dependent is the edge's to_id — i.e.
	// p.DependsOnNodeID, which AddDependency passes to autoBlockNode as
	// dep.ToID (LinkDepPayload.DependsOnNodeID is set from dep.ToID at emit).
	if p.DepType == string(model.DepTypeBlocks) {
		if err := autoBlockNode(ctx, tx, a.store, p.DependsOnNodeID, a.clock); err != nil {
			return fmt.Errorf("apply link_dep %s: auto-block %s: %w", format.EventID(e.EventID), p.DependsOnNodeID, err)
		}
	}
	return nil
}

func (a syncApplier) applyUnlinkDep(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.UnlinkDepPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply unlink_dep %s: decode payload: %w", format.EventID(e.EventID), err)
	}
	depType := p.DepType
	if depType == "" {
		depType = string(model.DepTypeBlocks)
	}
	fromID, err := fromIDForDepEdge(ctx, tx, e)
	if err != nil {
		return fmt.Errorf("apply unlink_dep %s: %w", format.EventID(e.EventID), err)
	}
	if err := refusePendingNumber(ctx, tx, e.EventID, p.DependsOnNodeID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM dependencies WHERE from_id = ? AND to_id = ? AND dep_type = ?`,
		fromID, p.DependsOnNodeID, depType,
	); err != nil {
		return err
	}

	// MTIX-44: mirror the local RemoveDependency derived-state recompute
	// (dependency.go). Removing a `blocks` edge must auto-unblock the
	// dependent when it was its last unresolved blocker, exactly as the local
	// path does via autoUnblockNode. The dependent is the removed edge's
	// to_id (p.DependsOnNodeID); autoUnblockNode re-counts remaining blockers,
	// so it is a safe no-op when others remain.
	if depType == string(model.DepTypeBlocks) {
		if err := autoUnblockNode(ctx, tx, a.store, p.DependsOnNodeID, a.clock); err != nil {
			return fmt.Errorf("apply unlink_dep %s: auto-unblock %s: %w", format.EventID(e.EventID), p.DependsOnNodeID, err)
		}
	}
	return nil
}
