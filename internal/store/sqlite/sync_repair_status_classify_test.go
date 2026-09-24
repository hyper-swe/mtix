// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Which differences status repair applies (MTIX-95.6, review round 1).
//
// A pre-MTIX-95.2 replay always leaves the row of an older held workflow
// event, so only such a node is repaired as a replay. A difference in a
// column derived from the status (closed_at, leaf progress) while the status
// matches the winner is repaired too. Every other difference is flagged "not
// a replay; review" and is applied only with force: newer state can arrive
// without an event, for example through a replace import of a teammate's
// .mtix/tasks.json, and reverting it would emit an event that reverts the
// teammate on every replica.

// replaceImport replaces dst's store with src's export, as the automatic
// import of a changed .mtix/tasks.json does.
func replaceImport(t *testing.T, dst, src *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	data, err := src.Export(ctx, "", "test")
	require.NoError(t, err)
	_, err = dst.Import(ctx, data, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
}

// TestStatusRepair_ImportedNewerState_FlaggedAndAppliedOnlyWithForce is the
// round-1 S1 reproduction: A claims MTIX-1 (a local event), teammate B marks
// it done, and A replace-imports B's export, which emits no event. The
// stored done is the row of no older event, so it is flagged, not applied
// without force, and applied with it.
func TestStatusRepair_ImportedNewerState_FlaggedAndAppliedOnlyWithForce(t *testing.T) {
	ctx := context.Background()
	a, rawA := replicaWithNode(t)
	require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-a"))
	b, _ := mutationTestStore(t)
	replaceImport(t, b, a)
	require.NoError(t, b.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-b"))
	replaceImport(t, a, b)
	before, events := nodeRow(t, rawA, "MTIX-1"), eventCount(t, rawA)
	require.Equal(t, "done", before["status"])

	diffs, err := a.StatusRepairDiffs(ctx)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.True(t, diffs[0].Flagged)
	require.True(t, strings.HasPrefix(diffs[0].Reason, "not a replay; review"), diffs[0].Reason)
	require.Empty(t, diffs[0].MatchedEventID)
	require.Equal(t, sqlite.StatusRepairColumn{Column: "status", Current: repairText("done"), Expected: repairText("in_progress")},
		diffs[0].Columns[0])

	listed, applied, err := a.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)
	require.NoError(t, err)
	require.NotNil(t, listed)
	require.False(t, applied, "a flagged node is not applied without force")
	require.Equal(t, before, nodeRow(t, rawA, "MTIX-1"))
	require.Equal(t, events, eventCount(t, rawA), "nothing is emitted")

	_, applied, err = a.RepairNodeStatus(ctx, "MTIX-1", "repairer", true)
	require.NoError(t, err)
	require.True(t, applied, "force applies a flagged node")
	require.Equal(t, "in_progress", nodeRow(t, rawA, "MTIX-1")["status"])
}

// TestStatusRepair_ImportedStateEqualToOlderEvent_FlaggedByNewerActivity: an
// imported state can coincide with the row of an older event (B claims with
// the agent of A's older claim). The imported activity records a status
// change after the winner that matches the stored state, which a replay
// never writes, so the node is still flagged.
func TestStatusRepair_ImportedStateEqualToOlderEvent_FlaggedByNewerActivity(t *testing.T) {
	ctx := context.Background()
	a, rawA := replicaWithNode(t)
	require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-a"))
	require.NoError(t, a.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
	b, _ := mutationTestStore(t)
	b.SetClock(func() time.Time { return time.Now().UTC().Add(time.Minute) })
	replaceImport(t, b, a)
	require.NoError(t, b.ClaimNode(ctx, "MTIX-1", "agent-a"))
	replaceImport(t, a, b)

	diffs, err := a.StatusRepairDiffs(ctx)

	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.True(t, diffs[0].Flagged, diffs[0].Reason)
	require.Equal(t, "not a replay; review: a status change recorded after the winning event matches the stored state",
		diffs[0].Reason)
	_, applied, err := a.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)
	require.NoError(t, err)
	require.False(t, applied)
	require.Equal(t, "in_progress", nodeRow(t, rawA, "MTIX-1")["status"])
}

// TestStatusRepairDiffs_ReplayedCancelUnderCancelledAncestor_Flagged: a
// replayed own cancel is a replay, but while a live ancestor is cancelled it
// may equally be a cascade cancel of a node cancelled before, so it is
// flagged for review. A soft-deleted ancestor does not count.
func TestStatusRepairDiffs_ReplayedCancelUnderCancelledAncestor_Flagged(t *testing.T) {
	tests := []struct {
		name          string
		deleteParent  bool
		wantFlagged   bool
		wantReasonHas string
	}{
		{"live cancelled parent", false, true, "cancelled ancestor"},
		{"soft-deleted cancelled parent", true, false, "replay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := replicaWithNode(t)
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			require.NoError(t, s.CancelNode(ctx, "MTIX-1.1", "dropped", "agent-a", false))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1", model.StatusOpen, "reopen", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
			replayTransitionAsBefore952(t, raw, "MTIX-1.1", model.StatusCancelled)
			if tt.deleteParent {
				// The parent soft-deleted on its own.
				_, err := raw.Exec(`UPDATE nodes SET deleted_at = ? WHERE id = 'MTIX-1'`, replayStamp())
				require.NoError(t, err)
			}

			diffs, err := s.StatusRepairDiffs(ctx)

			require.NoError(t, err)
			require.Len(t, diffs, 1)
			require.Equal(t, "MTIX-1.1", diffs[0].NodeID)
			require.Equal(t, tt.wantFlagged, diffs[0].Flagged)
			require.Contains(t, diffs[0].Reason, tt.wantReasonHas)
		})
	}
}

// TestStatusRepairDiffs_WinnerRuleResiduals_NotDifferences: the residuals
// documented for MTIX-95.10 are not differences. Each case is a healthy
// local history whose row differs from the winner's table row only where a
// residual says it may: a later update_field on a workflow column, closed_at
// on the originator (invalidation and restore), and local writes that emit
// no event (an auto-block with its blocker unresolved, a cascade cancel).
func TestStatusRepairDiffs_WinnerRuleResiduals_NotDifferences(t *testing.T) {
	ctx := context.Background()
	blocks := func(from, to string) *model.Dependency {
		return &model.Dependency{FromID: from, ToID: to, DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}
	}
	tests := []struct {
		name  string
		setup func(t *testing.T, s *sqlite.Store, raw *sql.DB)
	}{
		{"auto-blocked after its claim, blocker unresolved", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			mustCreateNode(t, s, "MTIX-2", "")
			require.NoError(t, s.AddDependency(ctx, blocks("MTIX-2", "MTIX-1")))
		}},
		{"descendant cancelled by a cascade", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
		}},
		{"descendant cancelled by a cascade, ancestor reopened", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
		}},
		{"cancelled by a cascade under a done parent", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			mustCreateNode(t, s, "MTIX-1.1.1", "MTIX-1.1")
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1", model.StatusDone, "finished", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1.1", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
		}},
		{"status and agent_state set by update_field after the claim", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			done, stuck := model.StatusDone, model.AgentStateStuck
			require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Status: &done, AgentState: &stuck}))
		}},
		{"assignee changed after the claim", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			bob := "bob"
			require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Assignee: &bob}))
		}},
		{"own invalidation of an open node", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
		}},
		{"own pushed invalidation of an open node", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
			markEventsPushed(t, raw)
		}},
		{"own restore of an invalidated closed node, then a claim", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "restore", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-b"))
		}},
		{"local lifecycle and synced events", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
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
			s, raw := replicaWithNode(t)
			tt.setup(t, s, raw)

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
	require.NoError(t, s.DeferNode(ctx, "MTIX-1", nil, "later", "agent-a"))
	markEventsPushed(t, raw)
	require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "now", "agent-a"))
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	bob := "bob"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Assignee: &bob}))
	replayTransitionAsBefore952(t, raw, "MTIX-1", model.StatusDeferred)

	diffs, err := s.StatusRepairDiffs(ctx)
	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.False(t, diffs[0].Flagged, diffs[0].Reason)
	require.Equal(t, []sqlite.StatusRepairColumn{
		{Column: "status", Current: repairText("deferred"), Expected: repairText("in_progress")},
	}, diffs[0].Columns)

	_, applied, err := s.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)
	require.NoError(t, err)
	require.True(t, applied)
	row := nodeRow(t, raw, "MTIX-1")
	require.Equal(t, "in_progress", row["status"])
	require.Equal(t, "working", row["agent_state"])
	require.Equal(t, "bob", row["assignee"], "the later update_field owns the assignee")
}

// TestStatusRepairDiffs_OlderAssigneeUpdate_DoesNotOwnTheColumn: an
// update_field older than the winner does not own the column, so the
// winner's assignee is compared. No older event left the stored assignee,
// so the node is flagged.
func TestStatusRepairDiffs_OlderAssigneeUpdate_DoesNotOwnTheColumn(t *testing.T) {
	ctx := context.Background()
	s, raw := replicaWithNode(t)
	bob := "bob"
	require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Assignee: &bob}))
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	// The assignee set back to the older update's value, without an event.
	_, err := raw.Exec(`UPDATE nodes SET assignee = 'bob' WHERE id = 'MTIX-1'`)
	require.NoError(t, err)

	diffs, err := s.StatusRepairDiffs(ctx)

	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.True(t, diffs[0].Flagged)
	require.Equal(t, []sqlite.StatusRepairColumn{
		{Column: "assignee", Current: repairText("bob"), Expected: repairText("agent-a")},
	}, diffs[0].Columns)
}

// TestRepairNodeStatus_RepairEvent_OtherReplicaKeepsItsClosedAt: the repair
// event carries the winner's wall clock, so a replica that already holds the
// winner stamps the same closed_at when it applies the repair event.
func TestRepairNodeStatus_RepairEvent_OtherReplicaKeepsItsClosedAt(t *testing.T) {
	ctx := context.Background()
	a, rawA := replicaWithNode(t)
	require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-a"))
	require.NoError(t, a.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
	done := eventIDOf(t, rawA, "MTIX-1", model.OpTransitionStatus)
	// The done was made on 2026-09-01, well before the repair.
	_, err := rawA.Exec(`UPDATE sync_events SET wall_clock_ts = ? WHERE event_id = ?`,
		foreignWallClock().UnixMilli(), done)
	require.NoError(t, err)
	b, rawB := mutationTestStore(t)
	pullEvents(t, b, pushPendingOwnEvents(t, rawA))
	require.Equal(t, foreignClosedAt, nodeRow(t, rawB, "MTIX-1")["closed_at"])
	replayClaimAsBefore952(t, rawA, "MTIX-1", "agent-a")

	_, applied, err := a.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)
	require.NoError(t, err)
	require.True(t, applied)
	pullEvents(t, b, pushPendingOwnEvents(t, rawA))

	row := nodeRow(t, rawB, "MTIX-1")
	require.Equal(t, "done", row["status"])
	require.Equal(t, foreignClosedAt, row["closed_at"], "the other replica keeps the completion time")
}

// TestStatusRepairDiffs_StoredAssigneeOfNoOlderClaim_Flagged: the stored
// status is that of an older claim, but the stored assignee is not its
// agent, so no older event left this state and the node is flagged.
func TestStatusRepairDiffs_StoredAssigneeOfNoOlderClaim_Flagged(t *testing.T) {
	ctx := context.Background()
	s, raw := replicaWithNode(t)
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
	// In progress for an agent no claim names, written without an event.
	_, err := raw.Exec(`UPDATE nodes SET status = 'in_progress', assignee = 'carol', agent_state = 'working'
		WHERE id = 'MTIX-1'`)
	require.NoError(t, err)

	diffs, err := s.StatusRepairDiffs(ctx)

	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.True(t, diffs[0].Flagged)
	require.Equal(t, "not a replay; review: no older event left the stored state", diffs[0].Reason)
}

// TestStatusRepairDiffs_UnreadableActivity_Flagged: when the node's activity
// cannot be read, repair cannot rule out a newer status change, so a replay
// is flagged rather than repaired.
func TestStatusRepairDiffs_UnreadableActivity_Flagged(t *testing.T) {
	s, raw := s7Store(t)
	// An activity column that is not JSON.
	_, err := raw.Exec(`UPDATE nodes SET activity = '<<<not-json' WHERE id = 'MTIX-1'`)
	require.NoError(t, err)

	diffs, err := s.StatusRepairDiffs(context.Background())

	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.True(t, diffs[0].Flagged)
	require.Equal(t, "not a replay; review: the node's activity cannot be read", diffs[0].Reason)
}

// TestStatusRepairDiffs_BlockedOnlyByDeletedBlocker_Listed: only an
// unresolved live blocker makes a block a local write to leave alone. A node
// whose only blocker was soft-deleted is compared; no older event left it
// blocked, so it is flagged for review (mtix unblock clears such a block).
func TestStatusRepairDiffs_BlockedOnlyByDeletedBlocker_Listed(t *testing.T) {
	ctx := context.Background()
	s, raw := replicaWithNode(t)
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
	mustCreateNode(t, s, "MTIX-2", "")
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "MTIX-2", ToID: "MTIX-1", DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC(),
	}))
	// The blocker soft-deleted on its own, leaving the block behind.
	_, err := raw.Exec(`UPDATE nodes SET deleted_at = ? WHERE id = 'MTIX-2'`, replayStamp())
	require.NoError(t, err)

	diffs, err := s.StatusRepairDiffs(ctx)

	require.NoError(t, err)
	require.Len(t, diffs, 1)
	require.Equal(t, "MTIX-1", diffs[0].NodeID)
	require.True(t, diffs[0].Flagged)
	require.Equal(t, sqlite.StatusRepairColumn{Column: "status", Current: repairText("blocked"), Expected: repairText("in_progress")},
		diffs[0].Columns[0])
}
