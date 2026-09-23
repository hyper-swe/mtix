// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// Workflow last-writer-wins at ingest (MTIX-95.10; ADR-006 §4.4 and defect
// D2; review F-28, F-43; FR-18.9, FR-18.11).
//
// This file is the one definition of the workflow winner rule. The ingest
// path (dispatchWithLWW) uses it for every pulled event, and the status
// repair tool (MTIX-95.6) uses the same order and the same column table to
// re-derive workflow state from the log, so ingest and repair cannot
// disagree.
//
// # Workflow events
//
// claim, unclaim, transition_status and defer. Together they write one
// piece of per-node state, the node's workflow state. update_field on
// status, assignee, agent_state or defer_until is not a workflow event here:
// it keeps its per-field LWW register (see the residual below, and
// MTIX-95.22 for defer_until).
//
// # Winner order
//
// Among the workflow events of one node, the winner is the event with the
// highest key (lamport_clock, event_id): the higher lamport_clock wins, and
// on a tie the higher event_id wins, compared as a byte string (Go string
// comparison and SQLite's default BINARY collation agree). Event ids are
// unique, so the order is total and every replica holding the same events
// picks the same winner. The node is identified by uid when the event
// carries one and by node_id otherwise, as detectLWWOutcome scopes field
// history (ADR-003 §3).
//
// At ingest an incoming workflow event applies only if it beats every
// workflow event already held in the local sync_events log for its node:
// this replica's own events (pending, pushed or conflicted) and every
// mirrored foreign event, winners and losers alike. A local mutation needs
// no check: its Lamport clock is above every event this replica holds, so it
// is the winner when it is written.
//
// A losing event is still mirrored into sync_events, its clocks are merged
// and it is recorded in applied_events, so a re-pull is a no-op and later
// comparisons see it. It changes no node column and writes no sync_conflicts
// row: workflow state is not a user-authored field, and a conflict row for
// every lost claim race or late replay would be noise. (Surfacing lost claims
// to the losing agent is ADR-006 §4.6, phase 4.)
//
// # Known residual (phase 0)
//
//   - Secondary columns. assignee, agent_state, previous_status, progress and
//     defer_until are written only by the table rows that list them. When
//     events arrive out of key order, an event that was the winner when it
//     arrived may have written a secondary column that the final winner's row
//     does not write, so that column can differ from a strict key-order
//     replay and between replicas. Example: a claim by agent-a applies, then
//     a done with a higher key arrives; the assignee stays agent-a. A replica
//     that received the done first rejects the claim and keeps the assignee
//     it had. status and closed_at always converge. Full convergence arrives
//     with the phase-4 projector's composite workflow register (ADR-006 §4.4).
//   - defer_until. The ADR-006 §4.4 register clears defer_until on claim,
//     unclaim and transition, and a local claim clears it too. Ingest does
//     not: that is out of scope for phase 0 (defer --until is MTIX-95.22).
//   - update_field on status, assignee or agent_state keeps its own per-field
//     register and is not compared with workflow events.
//   - closed_at. The originating store stamps its own clock when the local
//     mutation runs (transition.go, cancel.go); replicas stamp the winning
//     event's wall_clock_ts, truncated to whole seconds. The emitter reads
//     the clock separately from the mutation, so the originator and replicas
//     can differ within that second. invalidated stamps closed_at on replicas
//     (as apply did before MTIX-95.10), while the local transition leaves it
//     as it was.
//   - updated_at stays the apply time, and ingest writes no activity entry.
//   - Local writes that emit no event (auto-block on a new dependency, the
//     descendants of a cascade cancel) are invisible to the winner check.

// workflowAction is what a winning workflow event does to one nodes column.
type workflowAction uint8

const (
	// wfKeep leaves the column as it is: the local mutation does not write it.
	wfKeep workflowAction = iota
	// wfClear sets the column to NULL.
	wfClear
	// wfSet sets the column from the event; workflowRule documents the value
	// per column.
	wfSet
)

// workflowRule is one row of the workflow winner table. status (= to) and
// updated_at are written by every row. For the other columns, wfSet takes:
//
//	assignee         the claim payload's agent_id
//	agent_state      working
//	previous_status  the transition payload's from-status
//	closed_at        the event's wall_clock_ts, RFC3339 in whole seconds (UTC)
//	progress         1.0
//	defer_until      the defer payload's until (NULL when absent)
type workflowRule struct {
	op             model.OpType
	from           model.Status // transition_status: the payload from-status this row requires; "" matches any
	to             model.Status // the status written; transition_status rows match the payload to-status
	assignee       workflowAction
	agentState     workflowAction
	previousStatus workflowAction
	closedAt       workflowAction
	progress       workflowAction
	deferUntil     workflowAction
}

// workflowWinnerTable is the one documented table of the nodes columns a
// winning workflow event writes (MTIX-95.10; shared with MTIX-95.6). Rows are
// matched in order, so the specific blocked rows precede the generic ones.
// "-" means not written; "wall" is the event's wall_clock_ts as RFC3339 in
// whole seconds; "from" and "until" come from the payload; every row also
// writes updated_at (the apply time).
//
//	op                 from → to              status       assignee  agent_state  previous_status  closed_at  progress  defer_until
//	claim              (any)                  in_progress  agent_id  working      -                NULL       -         -
//	unclaim            (any)                  open         NULL      NULL         -                NULL       -         -
//	defer              (any)                  deferred     -         -            -                NULL       -         until
//	transition_status  any → done             done         -         -            -                wall       1.0       -
//	transition_status  any → cancelled        cancelled    -         -            -                wall       -         -
//	transition_status  any → invalidated      invalidated  -         -            from             wall       -         -
//	transition_status  any → blocked          blocked      -         -            from             NULL       -         -
//	transition_status  blocked → open         open         -         -            NULL             NULL       -         -
//	transition_status  blocked → in_progress  in_progress  -         -            NULL             NULL       -         -
//	transition_status  any → open             open         -         -            -                NULL       -         -
//	transition_status  any → in_progress      in_progress  -         -            -                NULL       -         -
//	transition_status  any → deferred         deferred     -         -            -                NULL       -         -
//
// Derivation. Each row writes the columns its local mutation writes, with the
// values taken from the event: claim and unclaim from ClaimNode and
// UnclaimNode (claim.go); defer from applyDefer (no local code emits defer,
// D12); done, cancelled, invalidated, blocked and the reopen from
// buildTransitionClauses (transition.go) and applyCancelUpdate (cancel.go);
// the blocked → open / in_progress rows from autoUnblockNode
// (dependency.go), the only writer of those auto-only transitions, which
// clears previous_status.
//
// closed_at is the one column every row writes, so it behaves as a register:
// the winner decides it alone, whatever arrived before (a winner that left it
// alone would keep whatever an earlier-applied event wrote, and closed_at
// would depend on arrival order). Locally closed_at is stamped by done and
// cancelled and cleared by a reopen from them; every other local transition
// leaves it alone, which is NULL for a non-terminal status in every state
// the local state machine reaches except a restore after invalidating a
// closed node. The residual list above records where this differs.
func workflowWinnerTable() []workflowRule {
	return []workflowRule{
		{op: model.OpClaim, to: model.StatusInProgress, assignee: wfSet, agentState: wfSet, closedAt: wfClear},
		{op: model.OpUnclaim, to: model.StatusOpen, assignee: wfClear, agentState: wfClear, closedAt: wfClear},
		{op: model.OpDefer, to: model.StatusDeferred, deferUntil: wfSet, closedAt: wfClear},
		{op: model.OpTransitionStatus, to: model.StatusDone, closedAt: wfSet, progress: wfSet},
		{op: model.OpTransitionStatus, to: model.StatusCancelled, closedAt: wfSet},
		{op: model.OpTransitionStatus, to: model.StatusInvalidated, previousStatus: wfSet, closedAt: wfSet},
		{op: model.OpTransitionStatus, to: model.StatusBlocked, previousStatus: wfSet, closedAt: wfClear},
		{op: model.OpTransitionStatus, from: model.StatusBlocked, to: model.StatusOpen,
			previousStatus: wfClear, closedAt: wfClear},
		{op: model.OpTransitionStatus, from: model.StatusBlocked, to: model.StatusInProgress,
			previousStatus: wfClear, closedAt: wfClear},
		{op: model.OpTransitionStatus, to: model.StatusOpen, closedAt: wfClear},
		{op: model.OpTransitionStatus, to: model.StatusInProgress, closedAt: wfClear},
		{op: model.OpTransitionStatus, to: model.StatusDeferred, closedAt: wfClear},
	}
}

// isWorkflowOp reports whether op is a workflow event: claim, unclaim,
// transition_status or defer (MTIX-95.10).
func isWorkflowOp(op model.OpType) bool {
	switch op {
	case model.OpClaim, model.OpUnclaim, model.OpTransitionStatus, model.OpDefer:
		return true
	default:
		return false
	}
}

// workflowKeyBeats reports whether the key (aLamport, aID) wins over the key
// (bLamport, bID) in the workflow winner order (MTIX-95.10; MTIX-95.6 uses the
// same order): the higher lamport_clock wins; on a tie the higher event_id,
// compared as a byte string, wins. Equal keys (the same event) do not win.
func workflowKeyBeats(aLamport int64, aID string, bLamport int64, bID string) bool {
	if aLamport != bLamport {
		return aLamport > bLamport
	}
	return aID > bID
}

// workflowKey is the winner-order key of one held workflow event.
type workflowKey struct {
	lamport int64
	eventID string
}

// latestHeldWorkflowKey returns the key of the highest-keyed workflow event
// held in sync_events for the node e addresses, excluding e itself (which the
// caller has just mirrored). The node is matched by uid when e carries one,
// else by node_id (MTIX-95.10). found is false when no workflow event is held.
func latestHeldWorkflowKey(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (workflowKey, bool, error) {
	// Highest-keyed workflow event already held for this node, in the winner
	// order (lamport_clock, then event_id), excluding the incoming event.
	// Served by idx_sync_events_node; the uid variant below by
	// idx_sync_events_uid.
	query := `SELECT lamport_clock, event_id FROM sync_events
	           WHERE node_id = ? AND op_type IN (?, ?, ?, ?) AND event_id <> ?
	           ORDER BY lamport_clock DESC, event_id DESC LIMIT 1`
	scope := e.NodeID
	if e.UID != "" {
		// Same lookup scoped by the node's durable uid (ADR-003 §3), so a
		// renumbered node keeps one workflow history.
		query = `SELECT lamport_clock, event_id FROM sync_events
		          WHERE uid = ? AND op_type IN (?, ?, ?, ?) AND event_id <> ?
		          ORDER BY lamport_clock DESC, event_id DESC LIMIT 1`
		scope = e.UID
	}
	var k workflowKey
	err := tx.QueryRowContext(ctx, query, scope,
		string(model.OpClaim), string(model.OpUnclaim),
		string(model.OpTransitionStatus), string(model.OpDefer),
		e.EventID,
	).Scan(&k.lamport, &k.eventID)
	if errors.Is(err, sql.ErrNoRows) {
		return workflowKey{}, false, nil
	}
	if err != nil {
		return workflowKey{}, false, fmt.Errorf("latest held workflow event: %w", err)
	}
	return k, true, nil
}

// workflowEventWins reports whether the workflow event e beats every workflow
// event already held for its node (MTIX-95.10). With none held, e wins.
func workflowEventWins(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (bool, error) {
	held, found, err := latestHeldWorkflowKey(ctx, tx, e)
	if err != nil {
		return false, err
	}
	if !found {
		return true, nil
	}
	return workflowKeyBeats(e.LamportClock, e.EventID, held.lamport, held.eventID), nil
}

// lookupWorkflowRule returns the first table row for op (and, for
// transition_status, the payload's from- and to-status).
func lookupWorkflowRule(op model.OpType, from, to model.Status) (workflowRule, bool) {
	for _, r := range workflowWinnerTable() {
		if r.op != op {
			continue
		}
		if op != model.OpTransitionStatus {
			return r, true
		}
		if r.to == to && (r.from == "" || r.from == from) {
			return r, true
		}
	}
	return workflowRule{}, false
}

// workflowInput is what one winning workflow event contributes to its row of
// the table.
type workflowInput struct {
	op          model.OpType
	from, to    model.Status // transition_status payload
	agentID     string       // claim payload
	deferUntil  *time.Time   // defer payload; nil means none
	wallClockTS int64        // the event envelope's wall_clock_ts, in ms
	updatedAt   string       // the caller's updated_at value (apply time)
}

// workflowColumn is one resolved column write: write says whether the column
// is written at all, value is what is written (nil writes NULL).
type workflowColumn struct {
	write bool
	value any
}

// workflowWrite is a table row resolved against one event.
type workflowWrite struct {
	status         model.Status
	updatedAt      string
	assignee       workflowColumn
	agentState     workflowColumn
	previousStatus workflowColumn
	closedAt       workflowColumn
	progress       workflowColumn
	deferUntil     workflowColumn
}

// resolveWorkflowWrite looks up the table row for in and resolves its column
// values from the event (MTIX-95.10). A terminal closed_at is the event's
// wall_clock_ts in whole seconds, never the apply time, so every replica
// stamps the same value. Returns ErrInvalidInput when no row matches (an
// unknown or empty to-status).
func resolveWorkflowWrite(in workflowInput) (workflowWrite, error) {
	rule, ok := lookupWorkflowRule(in.op, in.from, in.to)
	if !ok {
		return workflowWrite{}, fmt.Errorf("no workflow winner rule for %s %q -> %q: %w",
			in.op, in.from, in.to, model.ErrInvalidInput)
	}
	var until any
	if in.deferUntil != nil {
		until = in.deferUntil.UTC().Format(time.RFC3339)
	}
	return workflowWrite{
		status:         rule.to,
		updatedAt:      in.updatedAt,
		assignee:       resolveColumn(rule.assignee, in.agentID),
		agentState:     resolveColumn(rule.agentState, string(model.AgentStateWorking)),
		previousStatus: resolveColumn(rule.previousStatus, string(in.from)),
		closedAt:       resolveColumn(rule.closedAt, wallClockSeconds(in.wallClockTS)),
		progress:       resolveColumn(rule.progress, 1.0),
		deferUntil:     resolveColumn(rule.deferUntil, until),
	}, nil
}

// resolveColumn applies one table action: wfSet writes v, wfClear writes
// NULL, wfKeep writes nothing.
func resolveColumn(a workflowAction, v any) workflowColumn {
	switch a {
	case wfSet:
		return workflowColumn{write: true, value: v}
	case wfClear:
		return workflowColumn{write: true}
	default:
		return workflowColumn{}
	}
}

// wallClockSeconds formats an event's wall_clock_ts (Unix ms) as RFC3339 UTC
// in whole seconds, the precision closed_at is stored in. The RFC3339 layout
// has no fractional part, so formatting truncates the milliseconds.
func wallClockSeconds(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// writeWorkflowColumns writes a resolved table row onto node id (MTIX-95.10).
// A soft-deleted or missing node matches no row and is left alone.
func writeWorkflowColumns(ctx context.Context, tx *sql.Tx, id string, w workflowWrite) error {
	// One fixed statement, every value bound: status and updated_at always;
	// each other column only when the row writes it (the CASE keeps the
	// stored value otherwise).
	_, err := tx.ExecContext(ctx, `
		UPDATE nodes SET
		  status          = ?,
		  updated_at      = ?,
		  assignee        = CASE WHEN ? THEN ? ELSE assignee END,
		  agent_state     = CASE WHEN ? THEN ? ELSE agent_state END,
		  previous_status = CASE WHEN ? THEN ? ELSE previous_status END,
		  closed_at       = CASE WHEN ? THEN ? ELSE closed_at END,
		  progress        = CASE WHEN ? THEN ? ELSE progress END,
		  defer_until     = CASE WHEN ? THEN ? ELSE defer_until END
		WHERE id = ? AND deleted_at IS NULL`,
		string(w.status), w.updatedAt,
		w.assignee.write, w.assignee.value,
		w.agentState.write, w.agentState.value,
		w.previousStatus.write, w.previousStatus.value,
		w.closedAt.write, w.closedAt.value,
		w.progress.write, w.progress.value,
		w.deferUntil.write, w.deferUntil.value,
		id,
	)
	if err != nil {
		return fmt.Errorf("write workflow columns of %s: %w", id, err)
	}
	return nil
}

// applyWorkflowWinner writes a winning workflow event's table row onto the
// node the event addresses and returns that node's current id (MTIX-95.10).
// Callers reach it only for an event that won (dispatchWithLWW).
func applyWorkflowWinner(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, in workflowInput) (string, error) {
	id, err := resolveNodeRef(ctx, tx, e)
	if err != nil {
		return "", err
	}
	w, err := resolveWorkflowWrite(in)
	if err != nil {
		return "", fmt.Errorf("apply %s %s: %w", e.OpType, e.EventID, err)
	}
	if err := writeWorkflowColumns(ctx, tx, id, w); err != nil {
		return "", fmt.Errorf("apply %s %s: %w", e.OpType, e.EventID, err)
	}
	return id, nil
}
