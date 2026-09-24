// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// Status repair (MTIX-95.6; ADR-006 §5.3 and the history of defect D2;
// FR-18.9, FR-18.11).
//
// Before MTIX-95.2 a pull re-applied this replica's own events, and before
// MTIX-95.10 every workflow event applied in arrival order, so a replayed or
// late older claim, unclaim, defer or status change could overwrite newer
// workflow state. Upgrading stops new damage but does not heal a node that
// was already reverted. Status repair re-derives each live node's workflow
// state from its local sync_events log with the ingest winner rule and lists,
// or repairs, every node whose stored state differs.
//
// # The rule (never a second one)
//
// A node's workflow events are its claim, unclaim, transition_status and
// defer events in the local log: those carrying the node's uid, and those
// carrying no uid whose node_id is the node's id (ADR-003 §3). update_field
// is not a workflow event. The winner is the well-formed event with the
// highest key by workflowKeyBeats; an event the workflow payload rule rejects
// (decodeWorkflowPayload) never counts, exactly as at ingest (MTIX-95.27).
// The derived state is the winner's row of workflowWinnerTable, resolved by
// resolveWorkflowWrite with the repair time as the apply time, so closed_at
// comes from closedAtFromWallClock and falls back as at ingest. A node without
// a well-formed workflow event is never listed or changed.
//
// # What is compared
//
// Of the columns the winner's row writes, only status, assignee, agent_state,
// closed_at and, for a leaf (a node without live children), progress.
// previous_status, defer_until and updated_at are written by a repair but
// never compared. closed_at is compared as set or NULL, not by value: its
// value legitimately differs (the originator's clock, the pull time of an
// event applied before MTIX-95.10, the fallback for a time out of range).
//
// The residuals of the winner rule (sync_workflow_winner.go) are not
// differences (sync_repair_status_rules.go):
//   - A later update_field on status, assignee or agent_state (one whose key
//     beats the winner) owns that column, which is neither compared nor
//     written (updateFieldOwnedColumns).
//   - closed_at on the originator: for a winner this replica emitted,
//     closed_at is compared and written only where the local mutation writes
//     it (originatorKeepsClosedAt).
//   - defer_until: a winner that is this replica's own deferral keeps the
//     stored wake time (resolveWorkflowWrite).
//   - Local writes that emit no event: a node auto-blocked on top of the
//     winner's state, or a descendant cancelled by a cascade, is left alone
//     (localWriteOwnsNode).
//   - An unknown to-status: its row writes the status alone, compared and
//     repaired as ingest applies it.
//
// # Repair
//
// RepairNodeStatus repairs one node in its own write transaction. It
// re-derives inside the transaction, so a change made since the listing is
// respected, and it takes its target from the log, not from the interactive
// state machine. It writes the winner's row (writeWorkflowColumns) without the
// columns a residual owns, records a status_change activity entry naming the
// winner, emits a transition_status event from the stored status to the
// derived one with the reason "sync repair" so other replicas converge, then
// recomputes the parent's progress and unblocks dependents as a local
// transition does. The emitted event becomes the node's winner and its row
// matches the repaired columns, so a second run finds nothing.

// statusRepairReason is the reason of a repair's transition_status event and
// the text of its activity entry (MTIX-95.6).
const statusRepairReason = "sync repair"

// StatusRepairColumn is one compared workflow column whose stored value
// differs from the value the node's winning workflow event derives
// (MTIX-95.6). A nil value is NULL.
type StatusRepairColumn struct {
	Column   string  `json:"column"`
	Current  *string `json:"current"`
	Expected *string `json:"expected"`
}

// StatusRepairDiff is one live node whose workflow state differs from the
// state its local workflow events derive (MTIX-95.6). WinnerLocal is true
// when this replica emitted the winning event.
type StatusRepairDiff struct {
	NodeID        string               `json:"node_id"`
	WinnerEventID string               `json:"winner_event_id"`
	WinnerOp      model.OpType         `json:"winner_op"`
	WinnerLocal   bool                 `json:"winner_local"`
	Columns       []StatusRepairColumn `json:"columns"`
}

// workflowQueryer is what a re-derivation reads through: a read transaction
// for a listing, the write transaction for a repair.
type workflowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// workflowWinner is the winning well-formed workflow event of one node.
type workflowWinner struct {
	key         workflowKey
	op          model.OpType
	payload     workflowPayload
	wallClockTS int64
	local       bool // this replica emitted it: pending, pushed or conflicted
}

// statusPlan is one node's re-derivation: its winner, the write that repairs
// it and the compared columns that differ.
type statusPlan struct {
	row    workflowRow
	winner workflowWinner
	write  workflowWrite
	diffs  []StatusRepairColumn
}

// diff returns the plan as a StatusRepairDiff.
func (p statusPlan) diff() StatusRepairDiff {
	return StatusRepairDiff{
		NodeID:        p.row.id,
		WinnerEventID: p.winner.key.eventID,
		WinnerOp:      p.winner.op,
		WinnerLocal:   p.winner.local,
		Columns:       p.diffs,
	}
}

// StatusRepairDiffs lists every live node whose workflow state differs from
// the state its local workflow events derive, in node id order (MTIX-95.6).
// It reads one consistent snapshot and writes nothing.
func (s *Store) StatusRepairDiffs(ctx context.Context) (diffs []StatusRepairDiff, err error) {
	tx, err := s.readDB.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("status repair: begin read: %w", err)
	}
	defer func() {
		if rbErr := tx.Rollback(); rbErr != nil && err == nil {
			diffs, err = nil, fmt.Errorf("status repair: end read: %w", rbErr)
		}
	}()
	rows, err := readWorkflowRows(ctx, tx)
	if err != nil {
		return nil, err
	}
	now := s.clock().UTC().Format(time.RFC3339)
	diffs = []StatusRepairDiff{}
	for _, row := range rows {
		plan, found, planErr := planStatusRepair(ctx, tx, row, now)
		if planErr != nil {
			return nil, fmt.Errorf("status repair %s: %w", row.id, planErr)
		}
		if found && len(plan.diffs) > 0 {
			diffs = append(diffs, plan.diff())
		}
	}
	return diffs, nil
}

// RepairNodeStatus repairs node id's workflow state from its local workflow
// events in one write transaction (MTIX-95.6; see the comment at the top of
// this file). It returns the differences it repaired, or nil when the node
// matches its events or has none. author is recorded on the activity entry
// and the emitted event. Returns ErrNotFound for a missing or soft-deleted
// node.
func (s *Store) RepairNodeStatus(ctx context.Context, id, author string) (*StatusRepairDiff, error) {
	var repaired *StatusRepairDiff
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		row, err := readWorkflowRow(ctx, tx, id)
		if err != nil {
			return err
		}
		now := s.clock().UTC()
		plan, found, err := planStatusRepair(ctx, tx, row, now.Format(time.RFC3339))
		if err != nil || !found || len(plan.diffs) == 0 {
			return err
		}
		if err := applyStatusRepair(ctx, tx, plan, author, now); err != nil {
			return err
		}
		d := plan.diff()
		repaired = &d
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("status repair %s: %w", id, err)
	}
	return repaired, nil
}

// applyStatusRepair writes plan onto its node and records it as a status
// change, in the caller's transaction (MTIX-95.6): the activity entry, the
// transition_status event, the parent's progress and the dependents, as
// executeTransitionTx does for a local transition.
func applyStatusRepair(ctx context.Context, tx *sql.Tx, plan statusPlan, author string, now time.Time) error {
	id, from, to := plan.row.id, plan.row.status, plan.write.status
	if err := writeWorkflowColumns(ctx, tx, id, plan.write); err != nil {
		return err
	}
	if err := appendActivityEntry(ctx, tx, id, model.ActivityEntry{
		ID:        fmt.Sprintf("act-%d", now.UnixNano()),
		Type:      model.ActivityTypeStatusChange,
		Author:    author,
		Text:      statusRepairReason,
		CreatedAt: now,
		Metadata: mustMarshal(map[string]string{
			"from_status": string(from), "to_status": string(to), "repair": "status",
			"winner_event_id": plan.winner.key.eventID, "winner_op": string(plan.winner.op),
		}),
	}); err != nil {
		return fmt.Errorf("record repair activity: %w", err)
	}
	payload, err := model.EncodePayload(&model.TransitionStatusPayload{From: from, To: to, Reason: statusRepairReason})
	if err != nil {
		return fmt.Errorf("encode repair event: %w", err)
	}
	if err := emitEvent(ctx, tx, emitParams{
		NodeID: id, ProjectCode: projectPrefixFromNodeID(id),
		OpType: model.OpTransitionStatus, Author: author, Payload: payload,
	}); err != nil {
		return err
	}
	if err := recalculateProgress(ctx, tx, plan.row.parentID); err != nil {
		return fmt.Errorf("recalculate progress after repair: %w", err)
	}
	if isResolvingStatus(to) {
		if err := unblockDependents(ctx, tx, id, author); err != nil {
			return fmt.Errorf("auto-unblock dependents after repair: %w", err)
		}
	}
	return nil
}

// planStatusRepair re-derives row's workflow state (MTIX-95.6). found is
// false when the node has no well-formed workflow event, or when a local
// write that emits no event owns its state; such a node is left alone.
func planStatusRepair(ctx context.Context, q workflowQueryer, row workflowRow, now string) (statusPlan, bool, error) {
	events, err := readNodeEvents(ctx, q, row)
	if err != nil {
		return statusPlan{}, false, err
	}
	winner, found := pickWorkflowWinner(events)
	if !found {
		return statusPlan{}, false, nil
	}
	w, _ := resolveWorkflowWrite(workflowInput{
		op: winner.op, from: winner.payload.from, to: winner.payload.to,
		agentID: winner.payload.agentID, deferUntil: winner.payload.deferUntil,
		wallClockTS: winner.wallClockTS, updatedAt: now, localWinner: winner.local,
	})
	owned, err := localWriteOwnsNode(ctx, q, row, w.status)
	if err != nil || owned {
		return statusPlan{}, false, err
	}
	kept := updateFieldOwnedColumns(events, winner)
	kept.closedAt = originatorKeepsClosedAt(winner)
	w = keepColumns(w, row, kept)
	leaf, err := isLeafNode(ctx, q, row.id)
	if err != nil {
		return statusPlan{}, false, err
	}
	return statusPlan{row: row, winner: winner, write: w, diffs: compareWorkflowRow(row, w, leaf)}, true, nil
}

// nodeEvent is one event of a node that status repair reads: a workflow
// event or an update_field.
type nodeEvent struct {
	key         workflowKey
	op          model.OpType
	payload     []byte
	wallClockTS int64
	local       bool // this replica emitted it: pending, pushed or conflicted
}

// readNodeEvents returns the workflow and update_field events of row's node
// in the local log (MTIX-95.6): those carrying its uid, and those carrying no
// uid addressed to its id (ADR-003 §3). Each half is served by its index
// (idx_sync_events_uid, idx_sync_events_node).
func readNodeEvents(ctx context.Context, q workflowQueryer, row workflowRow) (out []nodeEvent, err error) {
	ops := []any{
		string(model.OpClaim), string(model.OpUnclaim), string(model.OpTransitionStatus),
		string(model.OpDefer), string(model.OpUpdateField),
	}
	args := append(append(append([]any{row.uid}, ops...), row.id), ops...)
	// The node's events by uid, then its uid-less events by node_id. The
	// winner is chosen in Go (workflowKeyBeats), so no ORDER BY is needed.
	rows, err := q.QueryContext(ctx, `
		SELECT event_id, lamport_clock, op_type, payload, wall_clock_ts, sync_status FROM sync_events
		 WHERE uid = ? AND uid <> '' AND op_type IN (?, ?, ?, ?, ?)
		UNION ALL
		SELECT event_id, lamport_clock, op_type, payload, wall_clock_ts, sync_status FROM sync_events
		 WHERE node_id = ? AND uid IS NULL AND op_type IN (?, ?, ?, ?, ?)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read events: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			out, err = nil, fmt.Errorf("read events: close: %w", closeErr)
		}
	}()
	for rows.Next() {
		var (
			e              nodeEvent
			op, syncStatus string
		)
		if scanErr := rows.Scan(&e.key.eventID, &e.key.lamport, &op, &e.payload, &e.wallClockTS, &syncStatus); scanErr != nil {
			return nil, fmt.Errorf("read events: %w", scanErr)
		}
		e.op, e.local = model.OpType(op), syncStatus != string(model.SyncStatusApplied)
		out = append(out, e)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("read events: %w", iterErr)
	}
	return out, nil
}

// pickWorkflowWinner returns the winning well-formed workflow event among
// events (MTIX-95.6): the one whose key beats every other by
// workflowKeyBeats, skipping every event the workflow payload rule rejects,
// exactly as ingest does (MTIX-95.10, MTIX-95.27). found is false when there
// is none.
func pickWorkflowWinner(events []nodeEvent) (best workflowWinner, found bool) {
	for _, e := range events {
		if !isWorkflowOp(e.op) {
			continue
		}
		p, malformed := decodeWorkflowPayload(e.op, e.payload)
		if malformed != nil {
			continue // never the winner (MTIX-95.27)
		}
		if found && !workflowKeyBeats(e.key.lamport, e.key.eventID, best.key.lamport, best.key.eventID) {
			continue
		}
		best, found = workflowWinner{key: e.key, op: e.op, payload: p, wallClockTS: e.wallClockTS, local: e.local}, true
	}
	return best, found
}
