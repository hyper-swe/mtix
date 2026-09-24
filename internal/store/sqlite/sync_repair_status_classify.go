// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Which differences status repair applies, and the closed_at a winner this
// replica emitted leaves (MTIX-95.6, review round 1).
//
// A pre-MTIX-95.2 replay re-ran the v0.5.0-beta apply statement of an older
// event, which wrote that event's status and, for a claim or unclaim, its
// assignee and agent_state, and wrote no activity entry. So a replay always
// leaves the row of an older held workflow event (its table row, on those
// columns), and no status change recorded after the winner. Newer state that
// arrived without an event, such as a replace import of a teammate's
// .mtix/tasks.json, usually matches no older event, and its activity records
// the status change that made it. Only replays are repaired as such; a
// difference that is not a replay is flagged for review and applied only
// with force, because the repair event would revert it on every replica.

// repairVerdict is how one node's differences are classified.
type repairVerdict struct {
	flagged bool
	reason  string
	matched string // the older event whose row the stored state is (a replay)
}

// The reasons a StatusRepairDiff gives.
const (
	reasonReplay             = "replay: the stored state is the row of an older event"
	reasonDerived            = "derived fields: the stored status matches the winning event"
	reasonNoOlderRow         = "not a replay; review: no older event left the stored state"
	reasonNewerActivity      = "not a replay; review: a status change recorded after the winning event matches the stored state"
	reasonUnreadableActivity = "not a replay; review: the node's activity cannot be read"
	reasonCancelledAncestor  = "not a replay; review: cancelled under a cancelled ancestor, possibly by a cascade cancel"
)

// classifyStatusDiff classifies plan's differences (MTIX-95.6). closed_at and
// leaf progress alone are derived from the status, so they are fixed when the
// status, assignee and agent_state match. Otherwise the node is a replay when
// its stored state is the row of an older event in chain and no newer status
// activity matches it; a cancelled node under a live cancelled ancestor may
// be a cascade cancel and is flagged, as is every other difference.
func classifyStatusDiff(ctx context.Context, q workflowQueryer, plan statusPlan, chain []workflowWinner, kept keptColumns, now string) (repairVerdict, error) {
	if onlyDerivedColumns(plan.diffs) {
		return repairVerdict{reason: reasonDerived}, nil
	}
	row := plan.row
	if row.status == model.StatusCancelled && !plan.write.status.IsTerminal() {
		ancestor, err := hasCancelledAncestor(ctx, q, row.id)
		if err != nil {
			return repairVerdict{}, err
		}
		if ancestor {
			return repairVerdict{flagged: true, reason: reasonCancelledAncestor}, nil
		}
	}
	newer, readable := newerStatusActivity(row, plan.winner.wallClockTS)
	switch {
	case !readable:
		return repairVerdict{flagged: true, reason: reasonUnreadableActivity}, nil
	case newer:
		return repairVerdict{flagged: true, reason: reasonNewerActivity}, nil
	}
	if matched, ok := olderRowMatch(row, chain[1:], kept, now); ok {
		return repairVerdict{reason: reasonReplay, matched: matched}, nil
	}
	return repairVerdict{flagged: true, reason: reasonNoOlderRow}, nil
}

// onlyDerivedColumns reports whether every differing column is closed_at or
// progress, the columns derived from the status.
func onlyDerivedColumns(diffs []StatusRepairColumn) bool {
	for _, c := range diffs {
		if c.Column != "closed_at" && c.Column != "progress" {
			return false
		}
	}
	return true
}

// olderRowMatch returns the newest event in older (newest first) whose table
// row is the stored state on the columns a pre-MTIX-95.2 replay of it wrote:
// the status, and the assignee and agent_state where its row writes them and
// no later update_field owns them (MTIX-95.6).
func olderRowMatch(row workflowRow, older []workflowWinner, kept keptColumns, now string) (string, bool) {
	for _, e := range older {
		ew, _ := resolveWorkflowWrite(workflowInputOf(e, now))
		if ew.status != row.status {
			continue
		}
		if !kept.assignee && ew.assignee.write && !sameValue(row.assignee, ew.assignee.value) {
			continue
		}
		if !kept.agentState && ew.agentState.write && !sameValue(row.agentState, ew.agentState.value) {
			continue
		}
		return e.key.eventID, true
	}
	return "", false
}

// newerStatusActivity reports whether the node's newest activity entry that
// records a status (a status_change with a to_status, a claim or an unclaim)
// was made after the winner, at wallMS, and records the stored status
// (MTIX-95.6). A replay writes no activity, so such an entry means the stored
// state came from a change the event log does not hold. readable is false
// when the activity cannot be parsed.
func newerStatusActivity(row workflowRow, wallMS int64) (newer, readable bool) {
	if !row.activity.Valid || row.activity.String == "" {
		return false, true
	}
	var entries []model.ActivityEntry
	if err := json.Unmarshal([]byte(row.activity.String), &entries); err != nil {
		return false, false
	}
	found := false
	var newest model.ActivityEntry
	var newestStatus model.Status
	for _, e := range entries {
		status, ok := recordedStatus(e)
		if !ok || (found && e.CreatedAt.Before(newest.CreatedAt)) {
			continue
		}
		found, newest, newestStatus = true, e, status
	}
	return found && newest.CreatedAt.UnixMilli() > wallMS && newestStatus == row.status, true
}

// recordedStatus returns the status an activity entry records: in_progress
// for a claim, open for an unclaim, the to_status of a status_change.
func recordedStatus(e model.ActivityEntry) (model.Status, bool) {
	switch e.Type {
	case model.ActivityTypeClaim:
		return model.StatusInProgress, true
	case model.ActivityTypeUnclaim:
		return model.StatusOpen, true
	case model.ActivityTypeStatusChange:
		var meta map[string]any
		if json.Unmarshal(e.Metadata, &meta) != nil {
			return "", false
		}
		to, ok := meta["to_status"].(string)
		return model.Status(to), ok && to != ""
	default:
		return "", false
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

// originatorClosedAt returns the closed_at to compare and write for a node
// whose winner, chain[0], this replica emitted; a foreign winner's table
// column is returned as it is (MTIX-95.6). Locally closed_at is written by
// the mutation, not the table (buildTransitionClauses, applyCancelUpdate):
// done and cancelled set it, a reopen from them clears it, and a claim, an
// unclaim and every other transition leave it as it was. So the local
// chain is walked from the winner down, newest first:
//   - a local done or cancel sets it (its wall clock, as the table would);
//   - a local reopen from done or cancelled clears it;
//   - a local invalidation or restore from invalidated leaves it as it is,
//     the documented closed_at-on-the-originator residual (nothing written);
//   - a local claim, unclaim or other transition is passed over;
//   - a foreign event whose row clears closed_at clears it; one whose row
//     sets it is passed over, because a claim or status change below which
//     it sits could not follow a closed node, so it lost at ingest.
//
// With no closing event the node never had a closed_at.
func originatorClosedAt(chain []workflowWinner, table workflowColumn, now string) workflowColumn {
	if !chain[0].local {
		return table
	}
	for _, e := range chain {
		if !e.local {
			fw, _ := resolveWorkflowWrite(workflowInputOf(e, now))
			if fw.closedAt.write && fw.closedAt.value == nil {
				return workflowColumn{write: true}
			}
			continue
		}
		if e.op != model.OpTransitionStatus {
			continue
		}
		from, to := e.payload.from, e.payload.to
		switch {
		case to == model.StatusDone || to == model.StatusCancelled:
			return workflowColumn{write: true, value: closedAtFromWallClock(e.wallClockTS, now)}
		case to == model.StatusOpen && (from == model.StatusDone || from == model.StatusCancelled):
			return workflowColumn{write: true}
		case to == model.StatusInvalidated || from == model.StatusInvalidated:
			return workflowColumn{}
		}
	}
	return workflowColumn{write: true}
}

// alignWithRepairEvent makes w match the transition_status event a repair
// that changes the status emits (MTIX-95.6). That event becomes the node's
// winner, a local one, so the next run derives closed_at from it: a
// transition to open from done or cancelled is a reopen, which clears
// closed_at, even where the previous winner left it as it was.
func alignWithRepairEvent(w workflowWrite, row workflowRow) workflowWrite {
	if w.status != row.status && w.status == model.StatusOpen &&
		(row.status == model.StatusDone || row.status == model.StatusCancelled) {
		w.closedAt = workflowColumn{write: true}
	}
	return w
}
