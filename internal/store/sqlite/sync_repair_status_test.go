// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"
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
// damaged rows are seeded with the statements the pre-MTIX-95.2 pull ran,
// stamped with a pull time after the winner, because the current pull no
// longer produces them.

// repairTime is the store clock the repair tests inject, so the repair's own
// timestamps are known. It is in the past, so every activity entry it stamps
// is older than the events the tests emit.
func repairTime() time.Time {
	return time.Date(2026, 9, 20, 8, 30, 0, 0, time.UTC)
}

// repairText returns a pointer to s, for StatusRepairColumn values.
func repairText(s string) *string { return &s }

// replayStamp is the time a pre-MTIX-95.2 pull stamped: the pull ran after the
// winner was emitted, so it is later than the winner's wall clock.
func replayStamp() string {
	return time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
}

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
		string(model.StatusInProgress), agent, string(model.AgentStateWorking), replayStamp(), id)
	require.NoError(t, err)
}

// replayUnclaimAsBefore952 writes the v0.5.0-beta applyUnclaim statement.
func replayUnclaimAsBefore952(t *testing.T, raw *sql.DB, id string) {
	t.Helper()
	// The pre-MTIX-95.2 applyUnclaim UPDATE, verbatim.
	_, err := raw.Exec(`UPDATE nodes SET status = ?, assignee = NULL, agent_state = NULL, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`, string(model.StatusOpen), replayStamp(), id)
	require.NoError(t, err)
}

// replayTransitionAsBefore952 writes what a pull before MTIX-95.2 wrote when
// it replayed this replica's own transition to status to: the v0.5.0-beta
// applyTransitionStatus statement, which stamped closed_at with the pull time
// for a terminal status and cleared it otherwise. It returns that closed_at.
func replayTransitionAsBefore952(t *testing.T, raw *sql.DB, id string, to model.Status) sql.NullString {
	t.Helper()
	now := replayStamp()
	closedAt := sql.NullString{}
	if to.IsTerminal() {
		closedAt = sql.NullString{String: now, Valid: true}
	}
	// The pre-MTIX-95.2 applyTransitionStatus UPDATE, verbatim.
	_, err := raw.Exec(`UPDATE nodes SET status = ?, closed_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		string(to), closedAt, now, id)
	require.NoError(t, err)
	return closedAt
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

// eventIDOf returns the id of the newest event of op for node id.
func eventIDOf(t *testing.T, raw *sql.DB, id string, op model.OpType) string {
	t.Helper()
	var eventID string
	// The node's newest event of one op.
	require.NoError(t, raw.QueryRow(`SELECT event_id FROM sync_events WHERE node_id = ? AND op_type = ?
		ORDER BY lamport_clock DESC LIMIT 1`, id, string(op)).Scan(&eventID))
	return eventID
}

// eventCount returns the number of events in the local log.
func eventCount(t *testing.T, raw *sql.DB) int {
	t.Helper()
	return countRows(t, raw, `SELECT COUNT(*) FROM sync_events`)
}

// eventWallClock returns an event's wall_clock_ts, in Unix ms.
func eventWallClock(t *testing.T, raw *sql.DB, eventID string) int64 {
	t.Helper()
	var ms int64
	// The wall clock the event was stamped with.
	require.NoError(t, raw.QueryRow(
		`SELECT wall_clock_ts FROM sync_events WHERE event_id = ?`, eventID).Scan(&ms))
	return ms
}

// wallClockClosedAt is closed_at as the table stamps it from an event's
// wall_clock_ts: RFC 3339 UTC in whole seconds.
func wallClockClosedAt(t *testing.T, raw *sql.DB, eventID string) string {
	t.Helper()
	return time.UnixMilli(eventWallClock(t, raw, eventID)).UTC().Format(time.RFC3339)
}

// wallClockText is a winner's wall clock as the report prints it.
func wallClockText(t *testing.T, raw *sql.DB, eventID string) string {
	t.Helper()
	return time.UnixMilli(eventWallClock(t, raw, eventID)).UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// repairEvents returns the payloads of the transition_status events for id
// whose reason is the repair's.
func repairEvents(t *testing.T, raw *sql.DB, id string) []model.TransitionStatusPayload {
	t.Helper()
	// Every status-change event of one node, oldest first.
	rows, err := raw.Query(`SELECT payload FROM sync_events
		WHERE node_id = ? AND op_type = ? ORDER BY lamport_clock`, id, string(model.OpTransitionStatus))
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
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

// seedColumns overwrites columns of node id, as an imported row.
func seedColumns(t *testing.T, raw *sql.DB, id, status string, closedAt sql.NullString, progress float64) {
	t.Helper()
	// A row written without an event.
	_, err := raw.Exec(`UPDATE nodes SET status = ?, closed_at = ?, progress = ? WHERE id = ?`,
		status, closedAt, progress, id)
	require.NoError(t, err)
}

// lastActivity returns node id's newest activity entry.
func lastActivity(t *testing.T, raw *sql.DB, id string) model.ActivityEntry {
	t.Helper()
	var activity []model.ActivityEntry
	require.NoError(t, json.Unmarshal([]byte(nodeRow(t, raw, id)["activity"]), &activity))
	require.NotEmpty(t, activity)
	return activity[len(activity)-1]
}

// TestStatusRepairDiffs_S7ReplayRevert_ListsOneStatusDifference is the
// dry-run half of the S7 regression: the only difference is the reverted
// status, the stored state is the row of the older claim (a replay), and
// listing it writes nothing.
func TestStatusRepairDiffs_S7ReplayRevert_ListsOneStatusDifference(t *testing.T) {
	s, raw := s7Store(t)
	before, events := nodeRow(t, raw, "MTIX-1"), eventCount(t, raw)
	require.NotEqual(t, nullColumn, before["closed_at"], "the S7 revert leaves closed_at set")
	winner := eventIDOf(t, raw, "MTIX-1", model.OpTransitionStatus)

	diffs, err := s.StatusRepairDiffs(context.Background())

	require.NoError(t, err)
	require.Equal(t, []sqlite.StatusRepairDiff{{
		NodeID:          "MTIX-1",
		WinnerEventID:   winner,
		WinnerOp:        model.OpTransitionStatus,
		WinnerLocal:     true,
		WinnerWallClock: wallClockText(t, raw, winner),
		MatchedEventID:  eventIDOf(t, raw, "MTIX-1", model.OpClaim),
		Reason:          "replay: the stored state is the row of an older event",
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
// repair" and the winner's wall clock, an activity entry names the winner,
// the parent's progress is recomputed and the dependent is unblocked, all in
// one transaction.
func TestRepairNodeStatus_S7ReplayRevert_RestoresDoneAndClosedAt(t *testing.T) {
	s, raw := repairFixture(t)
	winner := eventIDOf(t, raw, "MTIX-1.1", model.OpTransitionStatus)
	events := eventCount(t, raw)

	repaired, applied, err := s.RepairNodeStatus(context.Background(), "MTIX-1.1", "repairer", false)

	require.NoError(t, err)
	require.True(t, applied)
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
	repairEvent := eventIDOf(t, raw, "MTIX-1.1", model.OpTransitionStatus)
	require.Equal(t, eventWallClock(t, raw, winner), eventWallClock(t, raw, repairEvent),
		"the repair event carries the winner's wall clock, so every replica stamps the same closed_at")
	require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM sync_events
		WHERE node_id = 'MTIX-1.1' AND sync_status = 'pending' AND author_id = 'repairer'`))
	require.Equal(t, events+2, eventCount(t, raw), "the repair event and the dependent's unblock")

	last := lastActivity(t, raw, "MTIX-1.1")
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
	_, _, err := s.RepairNodeStatus(ctx, "MTIX-1.1", "repairer", false)
	require.NoError(t, err)
	after, events := nodeRow(t, raw, "MTIX-1.1"), eventCount(t, raw)

	diffs, err := s.StatusRepairDiffs(ctx)
	require.NoError(t, err)
	require.Empty(t, diffs)

	again, applied, err := s.RepairNodeStatus(ctx, "MTIX-1.1", "repairer", true)
	require.NoError(t, err)
	require.Nil(t, again, "nothing left to repair")
	require.False(t, applied)
	require.Equal(t, after, nodeRow(t, raw, "MTIX-1.1"))
	require.Equal(t, events, eventCount(t, raw))
}

// TestStatusRepair_NodeWithoutWorkflowEvents_Untouched: a node whose log
// holds no workflow event (only its creation and a title edit) is never
// listed or changed, whatever its row says, even with force.
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
	repaired, applied, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer", true)
	require.NoError(t, err)
	require.Nil(t, repaired)
	require.False(t, applied)
	require.Equal(t, before, nodeRow(t, raw, "MTIX-1"))
	require.Equal(t, events, eventCount(t, raw))
}

// TestStatusRepair_DoneLeafProgress_SetToOneWithoutAnEvent: a done leaf whose
// progress is not 1.0 (a done applied by sync before MTIX-95.10 never set
// it) is listed as a derived-field fix and repaired to 1.0. The status does
// not change, so no event is emitted (other replicas keep their closed_at);
// a system activity entry records the repair. A node with a live child keeps
// its rollup; a soft-deleted child does not count.
func TestStatusRepair_DoneLeafProgress_SetToOneWithoutAnEvent(t *testing.T) {
	tests := []struct {
		name  string
		child string // "", "live" or "deleted"
		want  []sqlite.StatusRepairColumn
	}{
		{"leaf", "", []sqlite.StatusRepairColumn{
			{Column: "progress", Current: repairText("0.25"), Expected: repairText("1")}}},
		{"node with a live child", "live", nil},
		{"node whose only child is soft-deleted", "deleted", []sqlite.StatusRepairColumn{
			{Column: "progress", Current: repairText("0.25"), Expected: repairText("1")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := mutationTestStore(t)
			s.SetClock(repairTime)
			mustCreateNode(t, s, "MTIX-1", "")
			if tt.child != "" {
				mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			}
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			if tt.child == "deleted" {
				require.NoError(t, s.DeleteNode(ctx, "MTIX-1.1", false, "agent-a"))
			}
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
			require.False(t, diffs[0].Flagged)
			require.Equal(t, "derived fields: the stored status matches the winning event", diffs[0].Reason)
			events := eventCount(t, raw)
			_, applied, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)
			require.NoError(t, err)
			require.True(t, applied)
			require.Equal(t, "1", nodeRow(t, raw, "MTIX-1")["progress"])
			require.Equal(t, events, eventCount(t, raw), "the status did not change, so no event")
			last := lastActivity(t, raw, "MTIX-1")
			require.Equal(t, model.ActivityTypeSystem, last.Type)
			require.Equal(t, "sync repair", last.Text)
		})
	}
}

// TestRepairNodeStatus_DeferralWinner_DeferUntil covers the wake-time rule
// from the MTIX-95.22 review. A local defer stores its wake time beside a
// transition_status event that carries none, so when the winner is this
// replica's own deferral, pending or pushed, repair keeps the stored
// defer_until instead of clearing it as the table row does. Every other
// winner's row clears it: a deferral received through sync carries no wake
// time, and a claim leaves deferred, so repair clears a stale one, as ingest
// does.
func TestRepairNodeStatus_DeferralWinner_DeferUntil(t *testing.T) {
	wake := time.Date(2031, 1, 2, 3, 4, 5, 0, time.UTC)
	ownDeferral := func(pushed bool) func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
		return func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			ctx := context.Background()
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "later", "agent-a"))
			require.NoError(t, s.DeferNode(ctx, "MTIX-1", &wake, "later", "agent-a"))
			if pushed {
				markEventsPushed(t, raw)
			}
			replayClaimAsBefore952(t, raw, "MTIX-1", "agent-a")
		}
	}
	tests := []struct {
		name   string
		setup  func(t *testing.T, s *sqlite.Store, raw *sql.DB)
		status string
		want   string
	}{
		{"own pending deferral keeps the local wake time", ownDeferral(false), "deferred", wake.Format(time.RFC3339)},
		{"own pushed deferral keeps the local wake time", ownDeferral(true), "deferred", wake.Format(time.RFC3339)},
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
			ctx := context.Background()
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "later", "agent-a"))
			markEventsPushed(t, raw)
			deferral := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				transition(model.StatusOpen, model.StatusDeferred), latestLamport(t, raw, model.OpUnclaim)+5, "")
			deferral.WallClockTS = time.Now().UTC().Add(time.Minute).UnixMilli() // made after the unclaim
			pullEvents(t, s, []*model.SyncEvent{deferral})
			replayUnclaimAsBefore952(t, raw, "MTIX-1")
			_, err := raw.Exec(`UPDATE nodes SET defer_until = ? WHERE id = 'MTIX-1'`, wake.Format(time.RFC3339))
			require.NoError(t, err)
		}, "deferred", nullColumn},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := replicaWithNode(t)
			tt.setup(t, s, raw)
			require.NotEqual(t, tt.status, nodeRow(t, raw, "MTIX-1")["status"], "the fixture is damaged")

			repaired, applied, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)

			require.NoError(t, err)
			require.NotNil(t, repaired)
			require.True(t, applied, "a replay is repaired without force: %s", repaired.Reason)
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
			claim := eventIDOf(t, raw, "MTIX-1", model.OpClaim)
			pullEvents(t, s, []*model.SyncEvent{
				malformedEvent(t, "MTIX-1", c, 1000), malformedEvent(t, "MTIX-2", c, 1001),
			})
			seedColumns(t, raw, "MTIX-2", "done", sql.NullString{}, 0)

			diffs, err := s.StatusRepairDiffs(ctx)

			require.NoError(t, err)
			require.Len(t, diffs, 1, "MTIX-2 has no well-formed workflow event")
			require.Equal(t, "MTIX-1", diffs[0].NodeID)
			require.Equal(t, done, diffs[0].WinnerEventID, "the malformed event is never the winner")
			require.Equal(t, claim, diffs[0].MatchedEventID,
				"a malformed event is never the matched older event either")
			require.Equal(t, []sqlite.StatusRepairColumn{
				{Column: "status", Current: repairText("in_progress"), Expected: repairText("done")},
			}, diffs[0].Columns)
		})
	}
}

// TestRepairNodeStatus_ClosedAt_FromWallClockAsAtIngest: the repaired
// closed_at of a foreign winner is closedAtFromWallClock of its wall clock:
// whole seconds, or, for a wall clock outside years 1..9999, the repair time,
// as ingest falls back to the apply time. closed_at is compared set against
// NULL, so the repaired node is stable either way.
func TestRepairNodeStatus_ClosedAt_FromWallClockAsAtIngest(t *testing.T) {
	later := time.UnixMilli(time.Now().UTC().Add(time.Minute).UnixMilli() + 500).UTC() // made after the local start
	tests := []struct {
		name string
		wall time.Time
		want string
	}{
		{"wall clock in range", later, later.Format(time.RFC3339)},
		{"wall clock after year 9999", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
			repairTime().Format(time.RFC3339)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := replicaWithNode(t)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInProgress, "start", "agent-a"))
			markEventsPushed(t, raw)
			done := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				transition(model.StatusInProgress, model.StatusDone), latestLamport(t, raw, model.OpTransitionStatus)+5, "")
			done.WallClockTS = tt.wall.UnixMilli()
			pullEvents(t, s, []*model.SyncEvent{done})
			replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusInProgress)
			s.SetClock(repairTime)

			_, applied, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)

			require.NoError(t, err)
			require.True(t, applied)
			row := nodeRow(t, raw, "MTIX-1")
			require.Equal(t, "done", row["status"])
			require.Equal(t, tt.want, row["closed_at"])
			diffs, err := s.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			require.Empty(t, diffs)
		})
	}
}

// TestRepairNodeStatus_OwnEventReplays_RestoreTheWinner covers the other
// replays a pre-MTIX-95.2 pull performed on this replica's own events. Each
// left the row of an older event, so each is a replay and is repaired
// without force; closed_at of a local winner follows the local event chain.
// An event is emitted only when the status changes.
func TestRepairNodeStatus_OwnEventReplays_RestoreTheWinner(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		setup     func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString
		matched   model.OpType
		want      []string // column: stored -> expected, "<replay>" is the replay's stamp, "<wall>" the winner's closed_at
		row       map[string]string
		wantEvent bool
	}{
		{"a replayed start cleared closed_at of a done node", func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString {
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInProgress, "start", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			return replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusInProgress)
		}, model.OpTransitionStatus, []string{"status: in_progress -> done", "closed_at: NULL -> <wall>"},
			map[string]string{"status": "done", "progress": "1"}, true},
		{"a replayed cancel after a reopen", func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
			return replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusCancelled)
		}, model.OpTransitionStatus, []string{"status: cancelled -> open", "closed_at: <replay> -> NULL"},
			map[string]string{"status": "open", "closed_at": nullColumn}, true},
		{"a replayed done after a reopen and a new claim", func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			return replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusDone)
		}, model.OpTransitionStatus, []string{"status: done -> in_progress", "closed_at: <replay> -> NULL"},
			map[string]string{"status": "in_progress", "closed_at": nullColumn, "assignee": "agent-a"}, true},
		{"a replayed done after a teammate's reopen and a local claim", func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			markEventsPushed(t, raw)
			reopen := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				transition(model.StatusDone, model.StatusOpen), latestLamport(t, raw, model.OpTransitionStatus)+5, "")
			reopen.WallClockTS = time.Now().UTC().Add(time.Minute).UnixMilli()
			pullEvents(t, s, []*model.SyncEvent{reopen})
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			return replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusDone)
		}, model.OpTransitionStatus, []string{"status: done -> in_progress", "closed_at: <replay> -> NULL"},
			map[string]string{"status": "in_progress", "closed_at": nullColumn}, true},
		{"a replayed unclaim above a late foreign done that lost", func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			// A teammate's done stamped below the local claim: it loses at ingest.
			late := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				transition(model.StatusInProgress, model.StatusDone), latestLamport(t, raw, model.OpClaim)-1, lowEventID)
			pullEvents(t, s, []*model.SyncEvent{late})
			require.Equal(t, "in_progress", nodeRow(t, raw, "MTIX-1")["status"], "the late done lost")
			replayUnclaimAsBefore952(t, raw, "MTIX-1")
			return sql.NullString{}
		}, model.OpUnclaim, []string{"status: open -> in_progress", "assignee: NULL -> agent-a", "agent_state: NULL -> working"},
			map[string]string{"status": "in_progress", "closed_at": nullColumn, "assignee": "agent-a"}, true},
		{"a replayed cancel after a restore from invalidated", func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "restore", "agent-a"))
			return replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusCancelled)
		}, model.OpTransitionStatus, []string{"status: cancelled -> open", "closed_at: <replay> -> NULL"},
			map[string]string{"status": "open", "closed_at": nullColumn}, true},
		{"a replayed claim by another agent", func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-b"))
			replayClaimAsBefore952(t, raw, "MTIX-1", "agent-a")
			return sql.NullString{}
		}, model.OpClaim, []string{"assignee: agent-a -> agent-b"},
			map[string]string{"status": "in_progress", "assignee": "agent-b", "agent_state": "working"}, false},
		{"a replayed manual block after an unblock", func(t *testing.T, s *sqlite.Store, raw *sql.DB) sql.NullString {
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusBlocked, "waiting", "agent-a"))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "unblocked", "agent-a"))
			return replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusBlocked)
		}, model.OpTransitionStatus, []string{"status: blocked -> open"},
			map[string]string{"status": "open"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := replicaWithNode(t)
			replayed := tt.setup(t, s, raw)
			winner := latestLocalWorkflowEvent(t, raw, "MTIX-1")

			diffs, err := s.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			require.Len(t, diffs, 1)
			d := diffs[0]
			require.Equal(t, winner, d.WinnerEventID)
			require.False(t, d.Flagged, d.Reason)
			require.NotEmpty(t, d.MatchedEventID)
			require.Equal(t, string(tt.matched), matchedOp(t, raw, d.MatchedEventID))
			var got []string
			for _, c := range d.Columns {
				got = append(got, c.Column+": "+columnValue(c.Current)+" -> "+columnValue(c.Expected))
			}
			var want []string
			for _, w := range tt.want {
				w = strings.ReplaceAll(w, "<replay>", replayed.String)
				w = strings.ReplaceAll(w, "<wall>", wallClockClosedAt(t, raw, winner))
				want = append(want, w)
			}
			require.Equal(t, want, got)

			events := eventCount(t, raw)
			_, applied, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)
			require.NoError(t, err)
			require.True(t, applied)
			row := nodeRow(t, raw, "MTIX-1")
			for col, v := range tt.row {
				require.Equal(t, v, row[col], "column %s", col)
			}
			if tt.wantEvent {
				require.Len(t, repairEvents(t, raw, "MTIX-1"), 1)
			} else {
				require.Equal(t, events, eventCount(t, raw), "the status did not change, so no event")
			}
			again, err := s.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			require.Empty(t, again)
		})
	}
}

// columnValue renders a nullable column value for comparison.
func columnValue(v *string) string {
	if v == nil {
		return "NULL"
	}
	return *v
}

// matchedOp returns the op_type of event id.
func matchedOp(t *testing.T, raw *sql.DB, id string) string {
	t.Helper()
	var op string
	// The op of one event by primary key.
	require.NoError(t, raw.QueryRow(`SELECT op_type FROM sync_events WHERE event_id = ?`, id).Scan(&op))
	return op
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

// TestStatusRepair_SoftDeletedNode_NotListedNorRepaired: a soft-deleted node
// is not live, so it is neither listed nor repaired, whatever its events say.
func TestStatusRepair_SoftDeletedNode_NotListedNorRepaired(t *testing.T) {
	ctx := context.Background()
	s, raw := s7Store(t)
	require.NoError(t, s.DeleteNode(ctx, "MTIX-1", false, "agent-a"))

	diffs, err := s.StatusRepairDiffs(ctx)
	require.NoError(t, err)
	require.Empty(t, diffs)
	_, _, err = s.RepairNodeStatus(ctx, "MTIX-1", "repairer", true)
	require.ErrorIs(t, err, model.ErrNotFound)
	require.Equal(t, "in_progress", nodeRow(t, raw, "MTIX-1")["status"])
}

// TestRepairNodeStatus_MissingNode_ReturnsNotFound: a node that does not
// exist is not repaired.
func TestRepairNodeStatus_MissingNode_ReturnsNotFound(t *testing.T) {
	s, _ := mutationTestStore(t)
	_, _, err := s.RepairNodeStatus(context.Background(), "MTIX-9", "repairer", true)
	require.ErrorIs(t, err, model.ErrNotFound)
}

// TestRepairNodeStatus_StatusUnchanged_DoesNotUnblockDependents: a repair
// that leaves the status alone (here the progress of a done leaf) emits no
// event and does not run the unblock a status change runs, so a dependent
// left blocked keeps its state for mtix unblock.
func TestRepairNodeStatus_StatusUnchanged_DoesNotUnblockDependents(t *testing.T) {
	ctx := context.Background()
	s, raw := replicaWithNode(t)
	mustCreateNode(t, s, "MTIX-2", "")
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "MTIX-1", ToID: "MTIX-2", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC(),
	}))
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
	// A stale leaf progress and a dependent left blocked.
	_, err := raw.Exec(`UPDATE nodes SET progress = 0 WHERE id = 'MTIX-1'`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE nodes SET status = 'blocked', previous_status = 'open' WHERE id = 'MTIX-2'`)
	require.NoError(t, err)
	events := eventCount(t, raw)

	_, applied, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)

	require.NoError(t, err)
	require.True(t, applied)
	require.Equal(t, "1", nodeRow(t, raw, "MTIX-1")["progress"])
	require.Equal(t, "blocked", nodeRow(t, raw, "MTIX-2")["status"])
	require.Equal(t, events, eventCount(t, raw))
}
