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

	"github.com/hyper-swe/mtix/internal/model"
)

// The reading and comparison half of status repair (MTIX-95.6): a node's
// stored workflow state and events, the residuals of the winner rule that are
// not differences, and the column comparison. sync_repair_status.go describes
// the whole.

// workflowRow is a node's stored workflow state and activity.
type workflowRow struct {
	id, uid, parentID              string
	status                         model.Status
	assignee, agentState, closedAt sql.NullString
	activity                       sql.NullString
	progress                       float64
}

// scanWorkflowRow scans the columns the two workflow row queries select
// (rowScanner is declared in export.go).
func scanWorkflowRow(sc rowScanner) (workflowRow, error) {
	var (
		r                     workflowRow
		uid, parentID, status sql.NullString
		progress              sql.NullFloat64
	)
	if err := sc.Scan(&r.id, &uid, &parentID, &status, &r.assignee, &r.agentState,
		&r.closedAt, &r.activity, &progress); err != nil {
		return workflowRow{}, err
	}
	r.uid, r.parentID, r.status, r.progress = uid.String, parentID.String, model.Status(status.String), progress.Float64
	return r, nil
}

// readWorkflowRows reads the workflow state of every live node, in id order.
func readWorkflowRows(ctx context.Context, q workflowQueryer) (out []workflowRow, err error) {
	// Every live node's workflow columns and activity, in id order (the
	// report's order).
	rows, err := q.QueryContext(ctx, `
		SELECT id, uid, parent_id, status, assignee, agent_state, closed_at, activity, progress
		FROM nodes WHERE deleted_at IS NULL ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("status repair: read nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			out, err = nil, fmt.Errorf("status repair: read nodes: close: %w", closeErr)
		}
	}()
	for rows.Next() {
		r, scanErr := scanWorkflowRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("status repair: read nodes: %w", scanErr)
		}
		out = append(out, r)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("status repair: read nodes: %w", iterErr)
	}
	return out, nil
}

// readWorkflowRow reads the workflow state of live node id. Returns
// ErrNotFound for a missing or soft-deleted node.
func readWorkflowRow(ctx context.Context, q workflowQueryer, id string) (workflowRow, error) {
	// One live node's workflow columns and activity.
	r, err := scanWorkflowRow(q.QueryRowContext(ctx, `
		SELECT id, uid, parent_id, status, assignee, agent_state, closed_at, activity, progress
		FROM nodes WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return workflowRow{}, fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return workflowRow{}, fmt.Errorf("read node %s: %w", id, err)
	}
	return r, nil
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
	// The node's events by uid, then its uid-less events by node_id. They are
	// ordered in Go (workflowChain), so no ORDER BY is needed.
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

// workflowChain returns the well-formed workflow events among events, newest
// first by workflowKeyBeats (MTIX-95.6): the first is the node's winner,
// exactly as ingest picks it (MTIX-95.10), and every event the workflow
// payload rule rejects is left out, as at ingest (MTIX-95.27).
func workflowChain(events []nodeEvent) []workflowWinner {
	var chain []workflowWinner
	for _, e := range events {
		if !isWorkflowOp(e.op) {
			continue
		}
		p, malformed := decodeWorkflowPayload(e.op, e.payload)
		if malformed != nil {
			continue // never the winner, never an older row (MTIX-95.27)
		}
		chain = append(chain, workflowWinner{key: e.key, op: e.op, payload: p, wallClockTS: e.wallClockTS, local: e.local})
	}
	sort.Slice(chain, func(i, j int) bool {
		return workflowKeyBeats(chain[i].key.lamport, chain[i].key.eventID, chain[j].key.lamport, chain[j].key.eventID)
	})
	return chain
}

// localWriteOwnsNode reports whether a local write that emits no event
// changed the node after its winner, the residual in sync_workflow_winner.go
// (MTIX-95.6). Such a node is left alone:
//   - an auto-block (autoBlockNode, which blocks only an open or in_progress
//     node): the node is blocked, the derived status is open or in_progress,
//     and a blocker is still unresolved; any other blocked node is compared;
//   - a cascade cancel (cancelDescendants): the node is cancelled, the derived
//     status is not terminal, and the node has no cancel event of its own.
func localWriteOwnsNode(ctx context.Context, q workflowQueryer, row workflowRow, derived model.Status, chain []workflowWinner) (bool, error) {
	switch {
	case row.status == model.StatusBlocked && (derived == model.StatusOpen || derived == model.StatusInProgress):
		return hasUnresolvedBlocker(ctx, q, row.id)
	case row.status == model.StatusCancelled && !derived.IsTerminal():
		for _, e := range chain {
			if e.op == model.OpTransitionStatus && e.payload.to == model.StatusCancelled {
				return false, nil
			}
		}
		return true, nil
	default:
		return false, nil
	}
}

// hasUnresolvedBlocker reports whether a live blocks-dependency of id is not
// done, cancelled or invalidated, the count autoUnblockNode uses.
func hasUnresolvedBlocker(ctx context.Context, q workflowQueryer, id string) (bool, error) {
	var n int
	// Live blockers of the node that are not resolved.
	if err := q.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM dependencies d
		JOIN nodes n ON d.from_id = n.id
		WHERE d.to_id = ? AND d.dep_type = ?
		  AND n.status NOT IN (?, ?, ?)
		  AND n.deleted_at IS NULL`,
		id, string(model.DepTypeBlocks),
		string(model.StatusDone), string(model.StatusCancelled), string(model.StatusInvalidated),
	).Scan(&n); err != nil {
		return false, fmt.Errorf("count blockers of %s: %w", id, err)
	}
	return n > 0, nil
}

// keptColumns are the compared columns a later update_field owns: repair
// neither compares nor writes them.
type keptColumns struct {
	status, assignee, agentState bool
}

// updateFieldOwnedColumns returns the workflow columns whose newest writer is
// an update_field event in events that beats the winner (MTIX-95.6).
// update_field keeps its own per-field register (the residual in
// sync_workflow_winner.go), so such a column is not the winner's to repair.
// An update_field whose payload cannot be decoded wrote nothing and owns
// nothing.
func updateFieldOwnedColumns(events []nodeEvent, winner workflowWinner) keptColumns {
	var kept keptColumns
	for _, e := range events {
		var p model.UpdateFieldPayload
		if e.op != model.OpUpdateField ||
			!workflowKeyBeats(e.key.lamport, e.key.eventID, winner.key.lamport, winner.key.eventID) ||
			json.Unmarshal(e.payload, &p) != nil {
			continue
		}
		kept.status = kept.status || p.FieldName == "status"
		kept.assignee = kept.assignee || p.FieldName == "assignee"
		kept.agentState = kept.agentState || p.FieldName == "agent_state"
	}
	return kept
}

// keepColumns removes the kept columns from w, so a repair writes the stored
// value back (status) or leaves the column alone (the others).
func keepColumns(w workflowWrite, row workflowRow, kept keptColumns) workflowWrite {
	if kept.status {
		w.status = row.status
	}
	if kept.assignee {
		w.assignee = workflowColumn{}
	}
	if kept.agentState {
		w.agentState = workflowColumn{}
	}
	return w
}

// isLeafNode reports whether node id has no live children.
func isLeafNode(ctx context.Context, q workflowQueryer, id string) (bool, error) {
	var n int
	// Live children of the node.
	if err := q.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes WHERE parent_id = ? AND deleted_at IS NULL`, id,
	).Scan(&n); err != nil {
		return false, fmt.Errorf("count children of %s: %w", id, err)
	}
	return n == 0, nil
}

// compareWorkflowRow returns the compared columns of row that differ from w,
// in the order status, assignee, agent_state, closed_at, progress (MTIX-95.6).
// A column w does not write is not compared; closed_at is compared as set or
// NULL; progress only for a leaf.
func compareWorkflowRow(row workflowRow, w workflowWrite, leaf bool) []StatusRepairColumn {
	var diffs []StatusRepairColumn
	if w.status != row.status {
		diffs = append(diffs, StatusRepairColumn{"status", textOf(string(row.status)), textOf(string(w.status))})
	}
	if w.assignee.write && !sameValue(row.assignee, w.assignee.value) {
		diffs = append(diffs, StatusRepairColumn{"assignee", nullableText(row.assignee), columnText(w.assignee.value)})
	}
	if w.agentState.write && !sameValue(row.agentState, w.agentState.value) {
		diffs = append(diffs, StatusRepairColumn{"agent_state", nullableText(row.agentState), columnText(w.agentState.value)})
	}
	if w.closedAt.write && row.closedAt.Valid != (w.closedAt.value != nil) {
		diffs = append(diffs, StatusRepairColumn{"closed_at", nullableText(row.closedAt), columnText(w.closedAt.value)})
	}
	if want, ok := w.progress.value.(float64); leaf && w.progress.write && ok && row.progress != want {
		diffs = append(diffs, StatusRepairColumn{"progress", floatText(row.progress), floatText(want)})
	}
	return diffs
}

// sameValue reports whether a stored text column holds v (nil is NULL).
func sameValue(stored sql.NullString, v any) bool {
	s, isText := v.(string)
	if !isText {
		return !stored.Valid
	}
	return stored.Valid && stored.String == s
}

// textOf returns a pointer to s.
func textOf(s string) *string { return &s }

// nullableText returns a stored text column, nil for NULL.
func nullableText(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return textOf(v.String)
}

// columnText returns a resolved text column value, nil for NULL.
func columnText(v any) *string {
	if s, ok := v.(string); ok {
		return textOf(s)
	}
	return nil
}

// floatText formats a progress value in its shortest form.
func floatText(v float64) *string { return textOf(strconv.FormatFloat(v, 'g', -1, 64)) }
