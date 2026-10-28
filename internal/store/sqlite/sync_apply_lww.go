// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// LWW resolution per FR-18.11 / SYNC-DESIGN section 8.2 applies to
// these op_types only — they share the field-level conflict semantics
// where two concurrent events touching the same logical field need a
// deterministic winner. Other op_types have their own semantics
// (delete is monotonic; comments are append-only; deps are idempotent;
// workflow ops use the winner rule in sync_workflow_winner.go).
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
	HasPrior     bool   // true iff there's a prior event for the same field
	PriorEventID string // empty when HasPrior is false
	IncomingWins bool   // true iff the incoming event beats the prior on (lamport, ts, hash)
	FieldName    string // for the conflict log
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
	// Reject unknown update columns before even constructing the LWW lookup.
	if e.OpType == model.OpUpdateField && !allowedUpdateFields[fieldName] {
		return lwwOutcome{}, fmt.Errorf("LWW update_field %s: field %q not in whitelist: %w",
			format.EventID(e.EventID), fieldName, model.ErrInvalidInput)
	}

	query, args := lwwPriorQuery(e, fieldName)
	var (
		priorID   string
		priorLamp int64
		priorTS   int64 // wall_clock_ts: an integer tie-break key here, never a stored time (MTIX-95.26)
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

// lwwPriorQuery returns the query detectLWWOutcome runs for the highest-
// lamport prior event on e's (node, field), and its arguments. Scoped by
// uid when e carries one (ADR-003 §3, §10): a node's pre- and post-renumber
// events share a stable uid but DIFFERENT node_ids, so keying on node_id
// would split one history across a renumber and let a stale-path event
// win. Uid-less (old-CLI) events fall back to node_id exactly as before.
// update_field also matches payload->>'field_name'; for set_acceptance and
// set_prompt the op_type alone is enough.
//
// The uid variants require a non-empty uid so the partial
// idx_sync_events_uid can serve them, and write "+op_type" so the planner
// does not pick idx_sync_events_op_type, which reads every event of that
// op type on an unanalysed store (MTIX-95.47).
func lwwPriorQuery(e *model.SyncEvent, fieldName string) (string, []any) {
	if e.UID == "" {
		args := []any{e.NodeID, string(e.OpType), e.EventID}
		if e.OpType == model.OpUpdateField {
			// The node's latest prior change of this field, by number.
			return `SELECT event_id, lamport_clock, wall_clock_ts, author_machine_hash
			         FROM sync_events
			         WHERE node_id = ? AND op_type = ? AND event_id <> ?
			           AND json_extract(payload, '$.field_name') = ?
			         ORDER BY lamport_clock DESC, wall_clock_ts DESC, author_machine_hash ASC
			         LIMIT 1`, append(args, fieldName)
		}
		// The node's latest prior event of this op, by number.
		return `SELECT event_id, lamport_clock, wall_clock_ts, author_machine_hash
		         FROM sync_events
		         WHERE node_id = ? AND op_type = ? AND event_id <> ?
		         ORDER BY lamport_clock DESC, wall_clock_ts DESC, author_machine_hash ASC
		         LIMIT 1`, args
	}
	args := []any{e.UID, string(e.OpType), e.EventID}
	if e.OpType == model.OpUpdateField {
		// The node's latest prior change of this field, by uid.
		return `SELECT event_id, lamport_clock, wall_clock_ts, author_machine_hash
		         FROM sync_events
		         WHERE uid = ? AND uid <> '' AND +op_type = ? AND event_id <> ?
		           AND json_extract(payload, '$.field_name') = ?
		         ORDER BY lamport_clock DESC, wall_clock_ts DESC, author_machine_hash ASC
		         LIMIT 1`, append(args, fieldName)
	}
	// The node's latest prior event of this op, by uid.
	return `SELECT event_id, lamport_clock, wall_clock_ts, author_machine_hash
	         FROM sync_events
	         WHERE uid = ? AND uid <> '' AND +op_type = ? AND event_id <> ?
	         ORDER BY lamport_clock DESC, wall_clock_ts DESC, author_machine_hash ASC
	         LIMIT 1`, args
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
// sync_events table with sync_status='applied'. INSERT OR IGNORE is
// defensive idempotency only: an event already in sync_events (any
// sync_status, including this replica's own emitted events) never
// reaches this call, because the own-event rule in acknowledgeHeldEvent
// returns first (MTIX-95.2).
func (a syncApplier) mirrorIncomingEvent(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	vcJSON, err := json.Marshal(e.VectorClock)
	if err != nil {
		return fmt.Errorf("mirror %s: marshal VC: %w", format.EventID(e.EventID), err)
	}
	_, err = tx.ExecContext(ctx, `
		INSERT OR IGNORE INTO sync_events
		  (event_id, project_prefix, node_id, uid, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash, sync_status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.EventID, e.ProjectPrefix, e.NodeID, nullableString(e.UID), string(e.OpType), string(e.Payload),
		e.WallClockTS, e.LamportClock, string(vcJSON),
		e.AuthorID, e.AuthorMachineHash, string(model.SyncStatusApplied),
		a.clock().UTC().Format(time.RFC3339Nano),
	)
	return err
}

// dispatchWithLWW runs the LWW resolution pipeline (workflow ops: see
// sync_workflow_winner.go): skip the apply for a loser, else run
// dispatchApply, and record a field conflict row when applicable.
//
// Extracted from IdempotentApply to keep cyclomatic complexity below
// the package's lint threshold.
func dispatchWithLWW(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	return (syncApplier{clock: time.Now}).dispatchWithLWW(ctx, tx, event)
}

func (a syncApplier) dispatchWithLWW(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	if isWorkflowOp(event.OpType) {
		wins, err := workflowEventWins(ctx, tx, event)
		if err != nil {
			return fmt.Errorf("apply %s: workflow winner: %w", format.EventID(event.EventID), err)
		}
		if !wins {
			return nil
		}
		return a.dispatchApply(ctx, tx, event)
	}

	outcome, err := detectLWWOutcome(ctx, tx, event)
	if err != nil {
		return fmt.Errorf("apply %s: LWW: %w", format.EventID(event.EventID), err)
	}

	if outcome.HasPrior && !outcome.IncomingWins {
		if err := a.recordLocalConflict(ctx, tx,
			outcome.PriorEventID, event.EventID, event.NodeID, outcome.FieldName,
		); err != nil {
			return fmt.Errorf("apply %s: record loser conflict: %w", format.EventID(event.EventID), err)
		}
		return nil
	}

	if err := a.dispatchApply(ctx, tx, event); err != nil {
		return err
	}
	if outcome.HasPrior && outcome.IncomingWins {
		if err := a.recordLocalConflict(ctx, tx,
			event.EventID, outcome.PriorEventID, event.NodeID, outcome.FieldName,
		); err != nil {
			return fmt.Errorf("apply %s: record winner conflict: %w", format.EventID(event.EventID), err)
		}
	}
	return nil
}

// recordLocalConflict persists a row to the local sync_conflicts
// table for surfacing via mtix sync conflicts list (MTIX-15.7).
func (a syncApplier) recordLocalConflict(ctx context.Context, tx *sql.Tx, winnerID, loserID, nodeID, fieldName string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO sync_conflicts
		  (event_id_winner, event_id_loser, node_id, field_name, resolution, resolved_at)
		VALUES (?, ?, ?, ?, 'lww', ?)`,
		winnerID, loserID, nodeID, nullableString(fieldName),
		a.clock().UTC().Format(time.RFC3339Nano),
	)
	return err
}
