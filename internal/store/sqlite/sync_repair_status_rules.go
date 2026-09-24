// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
)

// The comparison half of status repair (MTIX-95.6): reading a node's stored
// workflow state, the residuals of the winner rule that are not differences,
// and the column comparison. sync_repair_status.go describes the whole.

// workflowRow is a node's stored workflow state.
type workflowRow struct {
	id, uid, parentID              string
	status                         model.Status
	assignee, agentState, closedAt sql.NullString
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
		&r.closedAt, &progress); err != nil {
		return workflowRow{}, err
	}
	r.uid, r.parentID, r.status, r.progress = uid.String, parentID.String, model.Status(status.String), progress.Float64
	return r, nil
}

// readWorkflowRows reads the workflow state of every live node, in id order.
func readWorkflowRows(ctx context.Context, q workflowQueryer) (out []workflowRow, err error) {
	// Every live node's workflow columns, in id order (the report's order).
	rows, err := q.QueryContext(ctx, `
		SELECT id, uid, parent_id, status, assignee, agent_state, closed_at, progress
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
	// One live node's workflow columns.
	r, err := scanWorkflowRow(q.QueryRowContext(ctx, `
		SELECT id, uid, parent_id, status, assignee, agent_state, closed_at, progress
		FROM nodes WHERE id = ? AND deleted_at IS NULL`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return workflowRow{}, fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return workflowRow{}, fmt.Errorf("read node %s: %w", id, err)
	}
	return r, nil
}

// localWriteOwnsNode reports whether a local write that emits no event
// changed the node after its winner, the residual in sync_workflow_winner.go
// (MTIX-95.6). Such a node is left alone:
//   - an auto-block (autoBlockNode, which blocks only an open or in_progress
//     node): the node is blocked and the derived status is open or
//     in_progress, so repair never unblocks a node while a blocker may still
//     be unresolved (mtix unblock re-derives a block from its blockers);
//   - a cascade cancel (cancelDescendants): the node is cancelled, the derived
//     status is not terminal, and an ancestor is cancelled.
func localWriteOwnsNode(ctx context.Context, q workflowQueryer, row workflowRow, derived model.Status) (bool, error) {
	switch {
	case row.status == model.StatusBlocked:
		return derived == model.StatusOpen || derived == model.StatusInProgress, nil
	case row.status == model.StatusCancelled && !derived.IsTerminal():
		return hasCancelledAncestor(ctx, q, row.id)
	default:
		return false, nil
	}
}

// hasCancelledAncestor reports whether a live ancestor of id, by dot-notation
// id as a cascade cancel selects descendants, is cancelled.
func hasCancelledAncestor(ctx context.Context, q workflowQueryer, id string) (bool, error) {
	for anc := model.ParseIDParent(id); anc != ""; anc = model.ParseIDParent(anc) {
		var n int
		// Is this ancestor live and cancelled?
		if err := q.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM nodes WHERE id = ? AND deleted_at IS NULL AND status = ?`,
			anc, string(model.StatusCancelled),
		).Scan(&n); err != nil {
			return false, fmt.Errorf("read ancestor %s: %w", anc, err)
		}
		if n > 0 {
			return true, nil
		}
	}
	return false, nil
}

// keptColumns are the compared columns a residual owns: repair neither
// compares nor writes them.
type keptColumns struct {
	status, assignee, agentState, closedAt bool
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

// originatorKeepsClosedAt reports whether closed_at is the originator's for
// this winner, the closed_at on the originator residual (MTIX-95.6). For an
// event this replica emitted, closed_at is what the local mutation left:
// done and cancelled set it and a reopen from them clears it
// (buildTransitionClauses, applyCancelUpdate), while an invalidation, a
// restore, every other transition, a claim and an unclaim leave it as it was.
func originatorKeepsClosedAt(w workflowWinner) bool {
	if !w.local {
		return false
	}
	if w.op != model.OpTransitionStatus {
		return true
	}
	from, to := w.payload.from, w.payload.to
	if to == model.StatusDone || to == model.StatusCancelled {
		return false
	}
	return to != model.StatusOpen || (from != model.StatusDone && from != model.StatusCancelled)
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
	if kept.closedAt {
		w.closedAt = workflowColumn{}
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
