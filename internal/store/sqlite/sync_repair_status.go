// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
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
//   - closed_at on the originator: for a winner this replica emitted, the
//     expected closed_at follows the local event chain; the documented
//     invalidation and restore cases leave it as it is
//     (originatorClosedAt).
//   - defer_until: a winner that is this replica's own deferral keeps the
//     stored wake time (resolveWorkflowWrite).
//   - Local writes that emit no event: a node auto-blocked while a blocker is
//     unresolved, and a node cancelled with no cancel event of its own (a
//     cascade cancel), are left alone (localWriteOwnsNode).
//   - An unknown to-status: its row writes the status alone, compared and
//     repaired as ingest applies it.
//
// # Replay, derived fix or flagged (sync_repair_status_classify.go)
//
// A pre-MTIX-95.2 replay always leaves the row of an older held workflow
// event. A node whose status differs is repaired only when its stored state
// is such a row and no status change recorded after the winner matches it;
// a cancelled node whose ancestor was cancelled after the winner (a possible
// cascade cancel) is flagged. A node whose status matches the winner and
// whose closed_at or leaf progress differs is a derived fix; one whose
// assignee or agent_state differs is flagged, because an update_field
// writes no activity. Every other node is flagged "not a replay; review" and
// is repaired only with force: newer state can arrive without an event (a
// replace import of a teammate's .mtix/tasks.json), and reverting it would
// revert the teammate everywhere.
//
// # Repair
//
// RepairNodeStatus repairs one node in its own write transaction. It
// re-derives inside the transaction, so a change made since the listing is
// respected, and it takes its target from the log, not from the interactive
// state machine. It writes the winner's row (writeWorkflowColumns) without the
// columns a residual owns and records an activity entry naming the winner.
// When the status changes it emits a transition_status event from the stored
// status to the derived one, with the reason "sync repair" and the winner's
// wall clock (never later than now, repairEventWallClock), so other replicas
// converge and derive the same closed_at, and
// it unblocks dependents; a repair that leaves the status alone emits
// nothing. It then recomputes the parent's progress. The emitted event
// becomes the node's winner and its row matches the repaired columns, so a
// second run finds nothing.

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
// when this replica emitted the winning event, and WinnerWallClock is the
// time it was made. MatchedEventID is the older event whose row the stored
// state is, for a replay. Flagged nodes are repaired only with force; Reason
// says why a node is a replay, a derived fix or flagged.
type StatusRepairDiff struct {
	NodeID          string               `json:"node_id"`
	WinnerEventID   string               `json:"winner_event_id"`
	WinnerOp        model.OpType         `json:"winner_op"`
	WinnerLocal     bool                 `json:"winner_local"`
	WinnerWallClock string               `json:"winner_wall_clock"`
	MatchedEventID  string               `json:"matched_event_id,omitempty"`
	Flagged         bool                 `json:"flagged"`
	Reason          string               `json:"reason"`
	Columns         []StatusRepairColumn `json:"columns"`
}

// workflowQueryer is what a re-derivation reads through: a read transaction
// for a listing, the write transaction for a repair.
type workflowQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// workflowWinner is one well-formed workflow event of a node; the first of a
// node's events in key order is its winner.
type workflowWinner struct {
	key         workflowKey
	op          model.OpType
	payload     workflowPayload
	wallClockTS int64
	local       bool // this replica emitted it: pending, pushed or conflicted
}

// statusPlan is one node's re-derivation: its winner, the write that repairs
// it, the compared columns that differ and how they are classified.
type statusPlan struct {
	row     workflowRow
	winner  workflowWinner
	write   workflowWrite
	diffs   []StatusRepairColumn
	verdict repairVerdict
}

// diff returns the plan as a StatusRepairDiff.
func (p statusPlan) diff() StatusRepairDiff {
	return StatusRepairDiff{
		NodeID:          p.row.id,
		WinnerEventID:   p.winner.key.eventID,
		WinnerOp:        p.winner.op,
		WinnerLocal:     p.winner.local,
		WinnerWallClock: time.UnixMilli(p.winner.wallClockTS).UTC().Format("2006-01-02T15:04:05.000Z07:00"),
		MatchedEventID:  p.verdict.matched,
		Flagged:         p.verdict.flagged,
		Reason:          p.verdict.reason,
		Columns:         p.diffs,
	}
}

// StatusRepairDiffs lists every live node whose workflow state differs from
// the state its local workflow events derive, in node id order, each with its
// classification (MTIX-95.6). It reads one consistent snapshot and writes
// nothing.
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
// this file). It returns the node's differences, or nil when it matches its
// events or has none, and whether it applied them: a flagged node is applied
// only with force. author is recorded on the activity entry and any emitted
// event. Returns ErrNotFound for a missing or soft-deleted node.
func (s *Store) RepairNodeStatus(ctx context.Context, id, author string, force bool) (*StatusRepairDiff, bool, error) {
	var (
		listed  *StatusRepairDiff
		applied bool
	)
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
		d := plan.diff()
		listed = &d
		if plan.verdict.flagged && !force {
			return nil
		}
		applied = true
		return applyStatusRepair(ctx, tx, plan, author, now)
	})
	if err != nil {
		return nil, false, fmt.Errorf("status repair %s: %w", id, err)
	}
	return listed, applied, nil
}

// applyStatusRepair writes plan onto its node in the caller's transaction
// (MTIX-95.6). A status change is recorded as executeTransitionTx records
// one: a status_change activity entry, a transition_status event (with the
// winner's wall clock, repairEventWallClock) and the dependents unblocked. A repair that leaves
// the status alone records a system activity entry and emits nothing. The
// parent's progress is recomputed either way.
func applyStatusRepair(ctx context.Context, tx *sql.Tx, plan statusPlan, author string, now time.Time) error {
	id, from, to := plan.row.id, plan.row.status, plan.write.status
	if err := writeWorkflowColumns(ctx, tx, id, plan.write); err != nil {
		return err
	}
	if err := appendActivityEntry(ctx, tx, id, repairActivity(plan, author, now)); err != nil {
		return fmt.Errorf("record repair activity: %w", err)
	}
	if from != to {
		payload, err := model.EncodePayload(&model.TransitionStatusPayload{From: from, To: to, Reason: statusRepairReason})
		if err != nil {
			return fmt.Errorf("encode repair event: %w", err)
		}
		if err := emitEvent(ctx, tx, emitParams{
			NodeID: id, ProjectCode: projectPrefixFromNodeID(id), OpType: model.OpTransitionStatus,
			Author: author, Payload: payload, WallClockTS: repairEventWallClock(plan.winner.wallClockTS, now),
		}); err != nil {
			return err
		}
	}
	if err := recalculateProgress(ctx, tx, plan.row.parentID); err != nil {
		return fmt.Errorf("recalculate progress after repair: %w", err)
	}
	if from != to && isResolvingStatus(to) {
		if err := unblockDependents(ctx, tx, id, author); err != nil {
			return fmt.Errorf("auto-unblock dependents after repair: %w", err)
		}
	}
	return nil
}

// repairEventWallClock is the wall_clock_ts of a repair event (MTIX-95.6):
// the winner's, so every replica that holds the winner derives the same
// closed_at from the repair event, but never later than now. A winner stamped
// by a clock that is ahead (or outside the years a timestamp can hold) would
// otherwise give a pending event the push validator rejects (more than
// validator.FutureTimestampGrace ahead), and a push never sends part of a
// batch, so every later push would fail.
func repairEventWallClock(winnerMS int64, now time.Time) int64 {
	if nowMS := now.UnixMilli(); winnerMS > nowMS {
		return nowMS
	}
	return winnerMS
}

// repairActivity is the activity entry of a repair (MTIX-95.6): a
// status_change when the status changes, a system entry naming the repaired
// columns otherwise. Both name the winning event.
func repairActivity(plan statusPlan, author string, now time.Time) model.ActivityEntry {
	from, to := plan.row.status, plan.write.status
	meta := map[string]string{
		"repair": "status", "winner_event_id": plan.winner.key.eventID, "winner_op": string(plan.winner.op),
	}
	entry := model.ActivityEntry{
		ID: fmt.Sprintf("act-%d", now.UnixNano()), Type: model.ActivityTypeStatusChange,
		Author: author, Text: statusRepairReason, CreatedAt: now,
	}
	if from != to {
		meta["from_status"], meta["to_status"] = string(from), string(to)
	} else {
		entry.Type = model.ActivityTypeSystem
		cols := make([]string, 0, len(plan.diffs))
		for _, c := range plan.diffs {
			cols = append(cols, c.Column)
		}
		meta["columns"] = strings.Join(cols, ",")
	}
	entry.Metadata = mustMarshal(meta)
	return entry
}

// planStatusRepair re-derives row's workflow state and classifies its
// differences (MTIX-95.6). found is false when the node has no well-formed
// workflow event, or when a local write that emits no event owns its state;
// such a node is left alone.
func planStatusRepair(ctx context.Context, q workflowQueryer, row workflowRow, now string) (statusPlan, bool, error) {
	events, err := readNodeEvents(ctx, q, row)
	if err != nil {
		return statusPlan{}, false, err
	}
	chain := workflowChain(events)
	if len(chain) == 0 {
		return statusPlan{}, false, nil
	}
	winner := chain[0]
	w, _ := resolveWorkflowWrite(workflowInputOf(winner, now))
	owned, err := localWriteOwnsNode(ctx, q, row, w.status, chain)
	if err != nil || owned {
		return statusPlan{}, false, err
	}
	kept := updateFieldOwnedColumns(events, winner)
	w = keepColumns(w, row, kept)
	w.closedAt = originatorClosedAt(chain, w.closedAt, now)
	w = alignWithRepairEvent(w, row)
	leaf, err := isLeafNode(ctx, q, row.id)
	if err != nil {
		return statusPlan{}, false, err
	}
	plan := statusPlan{row: row, winner: winner, write: w, diffs: compareWorkflowRow(row, w, leaf)}
	if len(plan.diffs) > 0 {
		if plan.verdict, err = classifyStatusDiff(ctx, q, plan, chain, kept, now); err != nil {
			return statusPlan{}, false, err
		}
	}
	return plan, true, nil
}

// workflowInputOf is the resolveWorkflowWrite input of one held workflow
// event, with now as the apply time (MTIX-95.6).
func workflowInputOf(e workflowWinner, now string) workflowInput {
	return workflowInput{
		op: e.op, from: e.payload.from, to: e.payload.to,
		agentID: e.payload.agentID, deferUntil: e.payload.deferUntil,
		wallClockTS: e.wallClockTS, updatedAt: now, localWinner: e.local,
	}
}
