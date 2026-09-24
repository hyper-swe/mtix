// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Status repair regressions for MTIX-95.6 (ADR-006 §5.3, D2 history).
//
// Before MTIX-95.2 a pull re-applied this replica's own events, so a replayed
// older claim, unclaim, defer or status change could revert newer workflow
// state (ADR-006 scenario S7: claim, push, done, pull left the node
// in_progress with closed_at still set). Upgrading stops new damage but does
// not heal a node that was already reverted. Status repair re-derives each
// node's workflow state from its local event log with the ingest winner rule
// (sync_workflow_winner.go) and reports, or repairs, every difference. The
// damaged rows are seeded directly with the statement the pre-MTIX-95.2 pull
// ran, because the current pull no longer produces them.

// repairTime is the store clock every repair test injects, so the repair's
// own timestamps are known.
func repairTime() time.Time {
	return time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC)
}

// repairText returns a pointer to s, for StatusRepairColumn values.
func repairText(s string) *string { return &s }

// markEventsPushed marks every pending event pushed, as a push does.
func markEventsPushed(t *testing.T, raw *sql.DB) {
	t.Helper()
	// A push moves every pending event of this replica to pushed.
	_, err := raw.Exec(`UPDATE sync_events SET sync_status = 'pushed' WHERE sync_status = 'pending'`)
	require.NoError(t, err)
}

// replayClaimAsBefore952 writes what a pull before MTIX-95.2 wrote when it
// replayed this replica's own claim by agent: the v0.5.0-beta applyClaim
// statement, which left closed_at, progress and defer_until as they were.
func replayClaimAsBefore952(t *testing.T, raw *sql.DB, id, agent string) {
	t.Helper()
	// The pre-MTIX-95.2 applyClaim UPDATE, verbatim.
	_, err := raw.Exec(`UPDATE nodes SET status = ?, assignee = ?, agent_state = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		string(model.StatusInProgress), agent, string(model.AgentStateWorking), "2026-09-01T00:00:00Z", id)
	require.NoError(t, err)
}

// replayTransitionAsBefore952 writes what a pull before MTIX-95.2 wrote when
// it replayed this replica's own transition to status to: the v0.5.0-beta
// applyTransitionStatus statement, which stamped closed_at for a terminal
// status and cleared it otherwise.
func replayTransitionAsBefore952(t *testing.T, raw *sql.DB, id string, to model.Status) {
	t.Helper()
	now := "2026-09-01T00:00:00Z"
	closedAt := sql.NullString{}
	if to.IsTerminal() {
		closedAt = sql.NullString{String: now, Valid: true}
	}
	// The pre-MTIX-95.2 applyTransitionStatus UPDATE, verbatim.
	_, err := raw.Exec(`UPDATE nodes SET status = ?, closed_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		string(to), closedAt, now, id)
	require.NoError(t, err)
}

// s7ReplayRevert builds ADR-006 scenario S7 as a pre-MTIX-95.2 pull left it:
// id was claimed by agent-a, the claim was pushed, id was marked done, and a
// pull that replayed the claim reverted it to in_progress with closed_at set.
func s7ReplayRevert(t *testing.T, s *sqlite.Store, raw *sql.DB, id string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, s.ClaimNode(ctx, id, "agent-a"))
	markEventsPushed(t, raw)
	require.NoError(t, s.TransitionStatus(ctx, id, model.StatusDone, "finished", "agent-a"))
	replayClaimAsBefore952(t, raw, id, "agent-a")
}

// s7Store returns a store whose MTIX-1 is the S7 replay revert.
func s7Store(t *testing.T) (*sqlite.Store, *sql.DB) {
	t.Helper()
	s, raw := mutationTestStore(t)
	s.SetClock(repairTime)
	mustCreateNode(t, s, "MTIX-1", "")
	s7ReplayRevert(t, s, raw, "MTIX-1")
	return s, raw
}

// eventIDOf returns the id of the only event of op for node id.
func eventIDOf(t *testing.T, raw *sql.DB, id string, op model.OpType) string {
	t.Helper()
	var eventID string
	// The single event of one op for one node.
	require.NoError(t, raw.QueryRow(
		`SELECT event_id FROM sync_events WHERE node_id = ? AND op_type = ?`, id, string(op)).Scan(&eventID))
	return eventID
}

// eventCount returns the number of events in the local log.
func eventCount(t *testing.T, raw *sql.DB) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM sync_events`)
}

// wallClockClosedAt is closed_at as the table stamps it from event id's
// wall_clock_ts: RFC 3339 UTC in whole seconds.
func wallClockClosedAt(t *testing.T, raw *sql.DB, eventID string) string {
	t.Helper()
	var ms int64
	// The wall clock the event was stamped with, in Unix ms.
	require.NoError(t, raw.QueryRow(
		`SELECT wall_clock_ts FROM sync_events WHERE event_id = ?`, eventID).Scan(&ms))
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}

// repairEvents returns the payloads of the transition_status events for id
// whose reason is the repair's.
func repairEvents(t *testing.T, raw *sql.DB, id string) []model.TransitionStatusPayload {
	t.Helper()
	// Every status-change event of one node, oldest first.
	rows, err := raw.Query(`SELECT payload FROM sync_events
		WHERE node_id = ? AND op_type = ? ORDER BY lamport_clock`, id, string(model.OpTransitionStatus))
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []model.TransitionStatusPayload
	for rows.Next() {
		var payload string
		require.NoError(t, rows.Scan(&payload))
		var p model.TransitionStatusPayload
		require.NoError(t, json.Unmarshal([]byte(payload), &p))
		if p.Reason == "sync repair" {
			out = append(out, p)
		}
	}
	require.NoError(t, rows.Err())
	return out
}

// seedColumns overwrites columns of node id, as a damaged or imported row.
func seedColumns(t *testing.T, raw *sql.DB, id, status string, closedAt sql.NullString, progress float64) {
	t.Helper()
	// A damaged row, written directly.
	_, err := raw.Exec(`UPDATE nodes SET status = ?, closed_at = ?, progress = ? WHERE id = ?`,
		status, closedAt, progress, id)
	require.NoError(t, err)
}

// TestStatusRepairDiffs_S7ReplayRevert_ListsOneStatusDifference is the
// dry-run half of the S7 regression: the only difference is the reverted
// status, and listing it writes nothing.
func TestStatusRepairDiffs_S7ReplayRevert_ListsOneStatusDifference(t *testing.T) {
	s, raw := s7Store(t)
	before, events := nodeRow(t, raw, "MTIX-1"), eventCount(t, raw)
	require.NotEqual(t, nullColumn, before["closed_at"], "the S7 revert leaves closed_at set")

	diffs, err := s.StatusRepairDiffs(context.Background())

	require.NoError(t, err)
	require.Equal(t, []sqlite.StatusRepairDiff{{
		NodeID:        "MTIX-1",
		WinnerEventID: eventIDOf(t, raw, "MTIX-1", model.OpTransitionStatus),
		WinnerOp:      model.OpTransitionStatus,
		WinnerLocal:   true,
		Columns: []sqlite.StatusRepairColumn{
			{Column: "status", Current: repairText("in_progress"), Expected: repairText("done")},
		},
	}}, diffs)
	require.Equal(t, before, nodeRow(t, raw, "MTIX-1"), "a dry run changes no column")
	require.Equal(t, events, eventCount(t, raw), "a dry run emits no event")
}

// repairFixture is an S7 revert of MTIX-1.1 under the epic MTIX-1, with
// MTIX-2 as its dependent, left blocked, and MTIX-1's progress left stale.
func repairFixture(t *testing.T) (*sqlite.Store, *sql.DB) {
	t.Helper()
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	s.SetClock(repairTime)
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
	mustCreateNode(t, s, "MTIX-2", "")
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "MTIX-1.1", ToID: "MTIX-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC(),
	}))
	s7ReplayRevert(t, s, raw, "MTIX-1.1")
	// Stale derived state around the reverted node: the dependent blocked
	// again (restorable to open) and the epic's rollup at zero.
	_, err := raw.Exec(`UPDATE nodes SET status = 'blocked', previous_status = 'open' WHERE id = 'MTIX-2'`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE nodes SET progress = 0 WHERE id = 'MTIX-1'`)
	require.NoError(t, err)
	return s, raw
}

// TestRepairNodeStatus_S7ReplayRevert_RestoresDoneAndClosedAt is the apply
// half: the node is done again with closed_at set, one transition_status
// event from the stored to the derived status carries the reason "sync
// repair", an activity entry names the winner, the parent's progress is
// recomputed and the dependent is unblocked, all in one transaction.
func TestRepairNodeStatus_S7ReplayRevert_RestoresDoneAndClosedAt(t *testing.T) {
	s, raw := repairFixture(t)
	winner := eventIDOf(t, raw, "MTIX-1.1", model.OpTransitionStatus)
	events := eventCount(t, raw)

	repaired, err := s.RepairNodeStatus(context.Background(), "MTIX-1.1", "repairer")

	require.NoError(t, err)
	require.NotNil(t, repaired)
	require.Equal(t, "MTIX-1.1", repaired.NodeID)
	require.Equal(t, winner, repaired.WinnerEventID)
	row := nodeRow(t, raw, "MTIX-1.1")
	require.Equal(t, "done", row["status"])
	require.Equal(t, wallClockClosedAt(t, raw, winner), row["closed_at"],
		"closed_at is the winner's wall clock in whole seconds")
	require.Equal(t, "agent-a", row["assignee"], "the done row does not write the assignee")
	require.Equal(t, repairTime().Format(time.RFC3339), row["updated_at"])

	require.Equal(t, []model.TransitionStatusPayload{{From: model.StatusInProgress, To: model.StatusDone, Reason: "sync repair"}},
		repairEvents(t, raw, "MTIX-1.1"), "one repair event, from the stored to the derived status")
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM sync_events
		WHERE node_id = 'MTIX-1.1' AND sync_status = 'pending' AND author_id = 'repairer'`))
	require.Equal(t, events+2, eventCount(t, raw), "the repair event and the dependent's unblock")

	var activity []model.ActivityEntry
	require.NoError(t, json.Unmarshal([]byte(row["activity"]), &activity))
	last := activity[len(activity)-1]
	require.Equal(t, model.ActivityTypeStatusChange, last.Type)
	require.Equal(t, "sync repair", last.Text)
	require.Equal(t, "repairer", last.Author)
	var meta map[string]string
	require.NoError(t, json.Unmarshal(last.Metadata, &meta))
	require.Equal(t, map[string]string{
		"from_status": "in_progress", "to_status": "done", "repair": "status",
		"winner_event_id": winner, "winner_op": "transition_status",
	}, meta)

	require.Equal(t, "1", nodeRow(t, raw, "MTIX-1")["progress"], "the parent's progress is recomputed")
	require.Equal(t, "open", nodeRow(t, raw, "MTIX-2")["status"], "the dependent is unblocked")
}

// TestStatusRepairDiffs_AfterRepair_ReportsNothing: the repair event is the
// node's new winner and matches the repaired row, so a second run finds no
// difference and a second repair writes nothing.
func TestStatusRepairDiffs_AfterRepair_ReportsNothing(t *testing.T) {
	ctx := context.Background()
	s, raw := repairFixture(t)
	_, err := s.RepairNodeStatus(ctx, "MTIX-1.1", "repairer")
	require.NoError(t, err)
	after, events := nodeRow(t, raw, "MTIX-1.1"), eventCount(t, raw)

	diffs, err := s.StatusRepairDiffs(ctx)
	require.NoError(t, err)
	require.Empty(t, diffs)

	again, err := s.RepairNodeStatus(ctx, "MTIX-1.1", "repairer")
	require.NoError(t, err)
	require.Nil(t, again, "nothing left to repair")
	require.Equal(t, after, nodeRow(t, raw, "MTIX-1.1"))
	require.Equal(t, events, eventCount(t, raw))
}

// TestStatusRepair_NodeWithoutWorkflowEvents_Untouched: a node whose log
// holds no workflow event (only its creation and a title edit) is never
// listed or changed, whatever its row says.
func TestStatusRepair_NodeWithoutWorkflowEvents_Untouched(t *testing.T) {
	ctx := context.Background()
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	title := "renamed"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Title: &title}))
	// A status written without an event, as an import of tasks.json does.
	seedColumns(t, raw, "MTIX-1", "done", sql.NullString{}, 0)
	before, events := nodeRow(t, raw, "MTIX-1"), eventCount(t, raw)

	diffs, err := s.StatusRepairDiffs(ctx)
	require.NoError(t, err)
	require.Empty(t, diffs)
	repaired, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer")
	require.NoError(t, err)
	require.Nil(t, repaired)
	require.Equal(t, before, nodeRow(t, raw, "MTIX-1"))
	require.Equal(t, events, eventCount(t, raw))
}

// TestStatusRepair_DoneLeafProgress_SetToOne: a done leaf whose progress is
// not 1.0 (a done applied by sync before MTIX-95.10 never set it) is listed
// and repaired to 1.0; a done node with children keeps its rollup.
func TestStatusRepair_DoneLeafProgress_SetToOne(t *testing.T) {
	tests := []struct {
		name      string
		withChild bool
		want      []sqlite.StatusRepairColumn
	}{
		{"leaf", false, []sqlite.StatusRepairColumn{
			{Column: "progress", Current: repairText("0.25"), Expected: repairText("1")}}},
		{"node with a child", true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			if tt.withChild {
				mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			}
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			_, err := raw.Exec(`UPDATE nodes SET progress = 0.25 WHERE id = 'MTIX-1'`)
			require.NoError(t, err)

			diffs, err := s.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			if tt.want == nil {
				require.Empty(t, diffs)
				return
			}
			require.Len(t, diffs, 1)
			require.Equal(t, tt.want, diffs[0].Columns)
			_, err = s.RepairNodeStatus(ctx, "MTIX-1", "repairer")
			require.NoError(t, err)
			require.Equal(t, "1", nodeRow(t, raw, "MTIX-1")["progress"])
		})
	}
}

// TestRepairNodeStatus_DeferralWinner_DeferUntil covers the wake-time rule
// from the MTIX-95.22 review. A local defer stores its wake time beside a
// transition_status event that carries none, so when the winner is this
// replica's own deferral, repair keeps the stored defer_until instead of
// clearing it as the table row does. Every other winner's row clears it: a
// deferral received through sync carries no wake time, and a claim leaves
// deferred, so repair clears a stale one, as ingest does.
func TestRepairNodeStatus_DeferralWinner_DeferUntil(t *testing.T) {
	wake := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name   string
		setup  func(t *testing.T, s *sqlite.Store, raw *sql.DB)
		status string
		want   string
	}{
		{"own deferral keeps the local wake time", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			ctx := context.Background()
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "later", "agent-a"))
			require.NoError(t, s.DeferNode(ctx, "MTIX-1", &wake, "later", "agent-a"))
			replayClaimAsBefore952(t, raw, "MTIX-1", "agent-a")
		}, "deferred", wake.Format(time.RFC3339)},
		{"own claim after a deferral clears a stale wake time", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			ctx := context.Background()
			past := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
			require.NoError(t, s.DeferNode(ctx, "MTIX-1", &past, "later", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			// The replayed deferral, with the wake time an older mtix kept.
			replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusDeferred)
			_, err := raw.Exec(`UPDATE nodes SET defer_until = ? WHERE id = 'MTIX-1'`, wake.Format(time.RFC3339))
			require.NoError(t, err)
		}, "in_progress", nullColumn},
		{"foreign deferral clears a stale wake time", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			pullEvents(t, s, []*model.SyncEvent{foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				transition(model.StatusOpen, model.StatusDeferred), 5, "")})
			_, err := raw.Exec(`UPDATE nodes SET status = 'open', defer_until = ? WHERE id = 'MTIX-1'`,
				wake.Format(time.RFC3339))
			require.NoError(t, err)
		}, "deferred", nullColumn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := replicaWithNode(t)
			tt.setup(t, s, raw)
			require.NotEqual(t, tt.status, nodeRow(t, raw, "MTIX-1")["status"], "the fixture is damaged")

			repaired, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer")

			require.NoError(t, err)
			require.NotNil(t, repaired)
			row := nodeRow(t, raw, "MTIX-1")
			require.Equal(t, tt.status, row["status"])
			require.Equal(t, tt.want, row["defer_until"])
			diffs, err := s.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			require.Empty(t, diffs, "the repaired deferral is stable")
		})
	}
}

// TestStatusRepairDiffs_MalformedWorkflowEvents_ExcludedAsAtIngest: a
// malformed workflow event never counts as the winner (MTIX-95.27), so a
// newer malformed event neither hides the S7 revert nor changes the derived
// state, and a node whose only workflow events are malformed is untouched.
func TestStatusRepairDiffs_MalformedWorkflowEvents_ExcludedAsAtIngest(t *testing.T) {
	var cases []malformedCase
	cases = append(cases, malformedTransitions()...)
	cases = append(cases, malformedClaims()...)
	cases = append(cases, malformedDefers()...)
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := s7Store(t)
			mustCreateNode(t, s, "MTIX-2", "")
			done := eventIDOf(t, raw, "MTIX-1", model.OpTransitionStatus)
			pullEvents(t, s, []*model.SyncEvent{
				malformedEvent(t, "MTIX-1", c, 1000), malformedEvent(t, "MTIX-2", c, 1001),
			})
			seedColumns(t, raw, "MTIX-2", "done", sql.NullString{}, 0)

			diffs, err := s.StatusRepairDiffs(ctx)

			require.NoError(t, err)
			require.Len(t, diffs, 1, "MTIX-2 has no well-formed workflow event")
			require.Equal(t, "MTIX-1", diffs[0].NodeID)
			require.Equal(t, done, diffs[0].WinnerEventID, "the malformed event is never the winner")
			require.Equal(t, []sqlite.StatusRepairColumn{
				{Column: "status", Current: repairText("in_progress"), Expected: repairText("done")},
			}, diffs[0].Columns)
		})
	}
}

// TestRepairNodeStatus_ClosedAt_FromWallClockAsAtIngest: the repaired
// closed_at is closedAtFromWallClock of the winner: its wall clock in whole
// seconds, or, for a wall clock outside years 1..9999, the repair time, as
// ingest falls back to the apply time. closed_at is compared set against
// NULL, so the repaired node is stable either way.
func TestRepairNodeStatus_ClosedAt_FromWallClockAsAtIngest(t *testing.T) {
	tests := []struct {
		name string
		wall time.Time
		want string
	}{
		{"wall clock in range", foreignWallClock(), foreignClosedAt},
		{"wall clock after year 9999", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
			repairTime().Format(time.RFC3339)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := replicaWithNode(t)
			done := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				transition(model.StatusInProgress, model.StatusDone), 5, "")
			done.WallClockTS = tt.wall.UnixMilli()
			pullEvents(t, s, []*model.SyncEvent{done})
			seedColumns(t, raw, "MTIX-1", "in_progress", sql.NullString{}, 1)
			s.SetClock(repairTime)

			_, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer")

			require.NoError(t, err)
			row := nodeRow(t, raw, "MTIX-1")
			require.Equal(t, "done", row["status"])
			require.Equal(t, tt.want, row["closed_at"])
			diffs, err := s.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			require.Empty(t, diffs)
		})
	}
}

// TestStatusRepairDiffs_WinnerRuleResiduals_NotDifferences: the residuals
// documented for MTIX-95.10 are not differences. Each case is a healthy
// local history whose row differs from the winner's table row only where a
// residual says it may: a later update_field on a workflow column, closed_at
// on the originator, and local writes that emit no event.
func TestStatusRepairDiffs_WinnerRuleResiduals_NotDifferences(t *testing.T) {
	ctx := context.Background()
	blocks := func(from, to string) *model.Dependency {
		return &model.Dependency{FromID: from, ToID: to, DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, s *sqlite.Store)
	}{
		{"auto-blocked after its claim", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			mustCreateNode(t, s, "MTIX-2", "")
			require.NoError(t, s.AddDependency(ctx, blocks("MTIX-2", "MTIX-1")))
		}},
		{"descendant cancelled by a cascade", func(t *testing.T, s *sqlite.Store) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
		}},
		{"cancelled by a cascade under a done parent", func(t *testing.T, s *sqlite.Store) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			mustCreateNode(t, s, "MTIX-1.1.1", "MTIX-1.1")
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1", model.StatusDone, "finished", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1.1", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
		}},
		{"status and agent_state set by update_field after the claim", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			done, stuck := model.StatusDone, model.AgentStateStuck
			require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Status: &done, AgentState: &stuck}))
		}},
		{"assignee changed after the claim", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			bob := "bob"
			require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Assignee: &bob}))
		}},
		{"own invalidation of an open node", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
		}},
		{"own restore of an invalidated closed node, then a claim", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "restore", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-b"))
		}},
		{"local lifecycle and synced events", func(t *testing.T, s *sqlite.Store) {
			wake := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
			require.NoError(t, s.DeferNode(ctx, "MTIX-1", &wake, "later", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "now", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
			mustCreateNode(t, s, "MTIX-3", "")
			pullEvents(t, s, []*model.SyncEvent{
				foreignWorkflowEvent(t, "MTIX-3", model.OpClaim, &model.ClaimPayload{AgentID: "agent-b"}, 900, ""),
				foreignWorkflowEvent(t, "MTIX-3", model.OpTransitionStatus,
					transition(model.StatusInProgress, model.StatusDone), 901, ""),
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, _ := replicaWithNode(t)
			tt.setup(t, s)

			diffs, err := s.StatusRepairDiffs(ctx)

			require.NoError(t, err)
			require.Empty(t, diffs)
		})
	}
}

// TestRepairNodeStatus_LaterAssigneeUpdate_KeptWhenStatusRepaired: a column
// whose newest writer is an update_field that beats the winner is left as it
// is when repair writes the winner's row for another column.
func TestRepairNodeStatus_LaterAssigneeUpdate_KeptWhenStatusRepaired(t *testing.T) {
	ctx := context.Background()
	s, raw := replicaWithNode(t)
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	bob := "bob"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Assignee: &bob}))
	// A reverted status, as an older replayed unclaim left it.
	_, err := raw.Exec(`UPDATE nodes SET status = 'open', agent_state = NULL WHERE id = 'MTIX-1'`)
	require.NoError(t, err)

	diffs, err := s.StatusRepairDiffs(ctx)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, []sqlite.StatusRepairColumn{
		{Column: "status", Current: repairText("open"), Expected: repairText("in_progress")},
		{Column: "agent_state", Current: nil, Expected: repairText("working")},
	}, diffs[0].Columns)

	_, err = s.RepairNodeStatus(ctx, "MTIX-1", "repairer")
	require.NoError(t, err)
	row := nodeRow(t, raw, "MTIX-1")
	require.Equal(t, "in_progress", row["status"])
	require.Equal(t, "working", row["agent_state"])
	require.Equal(t, "bob", row["assignee"], "the later update_field owns the assignee")
}

// TestRepairNodeStatus_MissingNode_ReturnsNotFound: a node that does not
// exist, or is soft-deleted, is not repaired.
func TestRepairNodeStatus_MissingNode_ReturnsNotFound(t *testing.T) {
	s, _ := mutationTestStore(t)
	_, err := s.RepairNodeStatus(context.Background(), "MTIX-9", "repairer")
	require.ErrorIs(t, err, model.ErrNotFound)
}

// TestRepairNodeStatus_OwnEventReplays_RestoreTheWinner covers the other
// replays a pre-MTIX-95.2 pull performed on this replica's own events: each
// reverted a column the winner's row writes, and repair restores exactly
// those columns.
func TestRepairNodeStatus_OwnEventReplays_RestoreTheWinner(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		setup func(t *testing.T, s *sqlite.Store, raw *sql.DB)
		want  []sqlite.StatusRepairColumn
		row   map[string]string
	}{
		{"a replayed start cleared closed_at of a done node", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInProgress, "start", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusInProgress)
		}, []sqlite.StatusRepairColumn{
			{Column: "status", Current: repairText("in_progress"), Expected: repairText("done")},
			{Column: "closed_at", Current: nil, Expected: repairText("<wall>")},
		}, map[string]string{"status": "done", "progress": "1"}},
		{"a replayed cancel after a reopen", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
			replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusCancelled)
		}, []sqlite.StatusRepairColumn{
			{Column: "status", Current: repairText("cancelled"), Expected: repairText("open")},
			{Column: "closed_at", Current: repairText("2026-09-01T00:00:00Z"), Expected: nil},
		}, map[string]string{"status": "open", "closed_at": nullColumn}},
		{"a replayed claim by another agent", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-b"))
			replayClaimAsBefore952(t, raw, "MTIX-1", "agent-a")
		}, []sqlite.StatusRepairColumn{
			{Column: "assignee", Current: repairText("agent-a"), Expected: repairText("agent-b")},
		}, map[string]string{"status": "in_progress", "assignee": "agent-b", "agent_state": "working"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := replicaWithNode(t)
			tt.setup(t, s, raw)
			winner := latestLocalWorkflowEvent(t, raw, "MTIX-1")

			diffs, err := s.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			require.Len(t, diffs, 1)
			require.Equal(t, winner, diffs[0].WinnerEventID)
			for i := range tt.want {
				if tt.want[i].Expected != nil && *tt.want[i].Expected == "<wall>" {
					tt.want[i].Expected = repairText(wallClockClosedAt(t, raw, winner))
				}
			}
			require.Equal(t, tt.want, diffs[0].Columns)

			_, err = s.RepairNodeStatus(ctx, "MTIX-1", "repairer")
			require.NoError(t, err)
			row := nodeRow(t, raw, "MTIX-1")
			for col, want := range tt.row {
				require.Equal(t, want, row[col], "column %s", col)
			}
			again, err := s.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			require.Empty(t, again)
		})
	}
}

// latestLocalWorkflowEvent returns the id of node id's newest workflow event.
func latestLocalWorkflowEvent(t *testing.T, raw *sql.DB, id string) string {
	t.Helper()
	var eventID string
	// The node's highest-keyed workflow event (lamport, then event_id).
	require.NoError(t, raw.QueryRow(`SELECT event_id FROM sync_events
		WHERE node_id = ? AND op_type IN ('claim', 'unclaim', 'transition_status', 'defer')
		ORDER BY lamport_clock DESC, event_id DESC LIMIT 1`, id).Scan(&eventID))
	return eventID
}

// TestStatusRepairDiffs_OlderAssigneeUpdate_DoesNotOwnTheColumn: an
// update_field older than the winner does not own the column, so the
// winner's assignee is compared and repaired.
func TestStatusRepairDiffs_OlderAssigneeUpdate_DoesNotOwnTheColumn(t *testing.T) {
	ctx := context.Background()
	s, raw := replicaWithNode(t)
	bob := "bob"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Assignee: &bob}))
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	// The assignee reverted to the older update's value.
	_, err := raw.Exec(`UPDATE nodes SET assignee = 'bob' WHERE id = 'MTIX-1'`)
	require.NoError(t, err)

	diffs, err := s.StatusRepairDiffs(ctx)

	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, []sqlite.StatusRepairColumn{
		{Column: "assignee", Current: repairText("bob"), Expected: repairText("agent-a")},
	}, diffs[0].Columns)
}
