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
// imported state can coincide with the row of an older event of this
// replica, so the replay signature matches. The imported activity records
// the teammate's status change, made after the winner, which a replay never
// writes, so the node is still flagged: for a claim, an unclaim, a cancel
// and a defer.
func TestStatusRepair_ImportedStateEqualToOlderEvent_FlaggedByNewerActivity(t *testing.T) {
	ctx := context.Background()
	// backdateWinner moves this replica's newest event a minute back, so the
	// teammate's change is plainly after it.
	backdateWinner := func(t *testing.T, raw *sql.DB) {
		t.Helper()
		_, err := raw.Exec(`UPDATE sync_events SET wall_clock_ts = wall_clock_ts - 60000
			WHERE lamport_clock = (SELECT MAX(lamport_clock) FROM sync_events)`)
		require.NoError(t, err)
	}
	tests := []struct {
		name     string
		local    func(t *testing.T, a *sqlite.Store, raw *sql.DB)
		teammate func(t *testing.T, b *sqlite.Store)
		stored   string
	}{
		{"a claim equal to an older claim", func(t *testing.T, a *sqlite.Store, raw *sql.DB) {
			require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, a.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
			backdateWinner(t, raw)
		}, func(t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.ClaimNode(ctx, "MTIX-1", "agent-a"))
		}, "in_progress"},
		{"an unclaim equal to an older unclaim", func(t *testing.T, a *sqlite.Store, raw *sql.DB) {
			require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, a.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
			require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-a"))
			backdateWinner(t, raw)
		}, func(t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
		}, "open"},
		{"a cancel equal to an older cancel", func(t *testing.T, a *sqlite.Store, raw *sql.DB) {
			require.NoError(t, a.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
			require.NoError(t, a.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
			backdateWinner(t, raw)
		}, func(t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.CancelNode(ctx, "MTIX-1", "dropped", "agent-b", false))
		}, "cancelled"},
		{"a defer equal to an older defer", func(t *testing.T, a *sqlite.Store, raw *sql.DB) {
			require.NoError(t, a.DeferNode(ctx, "MTIX-1", nil, "later", "agent-a"))
			require.NoError(t, a.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "now", "agent-a"))
			backdateWinner(t, raw)
		}, func(t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.DeferNode(ctx, "MTIX-1", nil, "later", "agent-b"))
		}, "deferred"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, rawA := replicaWithNode(t)
			tt.local(t, a, rawA)
			b, _ := mutationTestStore(t)
			b.SetClock(func() time.Time { return time.Now().UTC().Add(time.Minute) })
			replaceImport(t, b, a)
			tt.teammate(t, b)
			replaceImport(t, a, b)
			require.Equal(t, tt.stored, nodeRow(t, rawA, "MTIX-1")["status"])

			diffs, err := a.StatusRepairDiffs(ctx)

			require.NoError(t, err)
			require.Len(t, diffs, 1)
			require.True(t, diffs[0].Flagged, diffs[0].Reason)
			require.Equal(t, "not a replay; review: a status change recorded after the winning event matches the stored state",
				diffs[0].Reason)
			_, applied, err := a.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)
			require.NoError(t, err)
			require.False(t, applied)
			require.Equal(t, tt.stored, nodeRow(t, rawA, "MTIX-1")["status"])
		})
	}
}

// TestStatusRepairDiffs_CancelledNodeWithNewerAncestorCancel_Flagged: a
// cancelled node whose derived status is not terminal is flagged, never
// applied without force, when any ancestor holds a cancel event newer than
// the node's winner, whatever the ancestor's status is now: the node may
// have been cancelled by that ancestor's cascade. An ancestor cancel older
// than the winner does not count, so a replayed own cancel is repaired.
func TestStatusRepairDiffs_CancelledNodeWithNewerAncestorCancel_Flagged(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name        string
		ancestors   func(t *testing.T, s *sqlite.Store, raw *sql.DB) // after the node's cancel and reopen
		node        string
		wantFlagged bool
	}{
		{"a live cancelled parent", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
			replayTransitionAsBefore952(t, raw, "MTIX-1.1", model.StatusCancelled)
		}, "MTIX-1.1", true},
		{"a soft-deleted cancelled parent", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
			replayTransitionAsBefore952(t, raw, "MTIX-1.1", model.StatusCancelled)
			// The parent soft-deleted on its own.
			_, err := raw.Exec(`UPDATE nodes SET deleted_at = ? WHERE id = 'MTIX-1'`, replayStamp())
			require.NoError(t, err)
		}, "MTIX-1.1", true},
		{"a parent cascade, the parent reopened", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
		}, "MTIX-1.1", true},
		{"a grandparent cascade over a done parent, reopened", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
		}, "MTIX-1.1.1", true},
		{"a parent whose activity cannot be read", func(t *testing.T, _ *sqlite.Store, raw *sql.DB) {
			replayTransitionAsBefore952(t, raw, "MTIX-1.1", model.StatusCancelled)
			// An activity column that is not JSON.
			_, err := raw.Exec(`UPDATE nodes SET activity = '<<<not-json' WHERE id = 'MTIX-1'`)
			require.NoError(t, err)
		}, "MTIX-1.1", true},
		{"a teammate's parent cascade, imported without events", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			// The node's reopen was made a minute before the teammate's cascade.
			_, err := raw.Exec(`UPDATE sync_events SET wall_clock_ts = wall_clock_ts - 60000
				WHERE lamport_clock = (SELECT MAX(lamport_clock) FROM sync_events)`)
			require.NoError(t, err)
			b, _ := mutationTestStore(t)
			replaceImport(t, b, s)
			require.NoError(t, b.CancelNode(ctx, "MTIX-1", "dropped", "agent-b", true))
			replaceImport(t, s, b)
		}, "MTIX-1.1", true},
		{"a parent cancel older than the node's winner", nil, "MTIX-1.1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := replicaWithNode(t)
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			mustCreateNode(t, s, "MTIX-1.1.1", "MTIX-1.1")
			if tt.node == "MTIX-1.1.1" {
				require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
				require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1", model.StatusDone, "finished", "agent-a"))
			}
			if tt.ancestors == nil {
				// The parent was cancelled and reopened before the node's history.
				require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", false))
				require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
			}
			require.NoError(t, s.CancelNode(ctx, tt.node, "dropped", "agent-a", false))
			markEventsPushed(t, raw)
			require.NoError(t, s.TransitionStatus(ctx, tt.node, model.StatusOpen, "reopen", "agent-a"))
			if tt.ancestors != nil {
				tt.ancestors(t, s, raw)
			} else {
				replayTransitionAsBefore952(t, raw, tt.node, model.StatusCancelled)
			}
			require.Equal(t, "cancelled", nodeRow(t, raw, tt.node)["status"])

			diffs, err := s.StatusRepairDiffs(ctx)

			require.NoError(t, err)
			require.Len(t, diffs, 1)
			require.Equal(t, tt.node, diffs[0].NodeID)
			require.Equal(t, tt.wantFlagged, diffs[0].Flagged, diffs[0].Reason)
			_, applied, err := s.RepairNodeStatus(ctx, tt.node, "repairer", false)
			require.NoError(t, err)
			require.Equal(t, !tt.wantFlagged, applied)
			if tt.wantFlagged {
				require.Equal(t, "not a replay; review: an ancestor was cancelled after the winning event, possibly by a cascade cancel",
					diffs[0].Reason)
				require.Equal(t, "cancelled", nodeRow(t, raw, tt.node)["status"], "a flagged node is not applied without force")
			}
		})
	}
}

// TestStatusRepairDiffs_WinnerRuleResiduals_NeverAppliedWithoutForce: the
// residuals documented for MTIX-95.10 are not differences. Each case is a
// healthy local history whose row differs from the winner's table row only
// where a residual says it may: a later update_field on a workflow column,
// closed_at on the originator (invalidation and restore, also of a foreign
// invalidation), and local writes that emit no event (an auto-block with its
// blocker unresolved, a cascade cancel). A cascade over a node that had
// cancel events of its own is listed but flagged, so --apply without force
// leaves it alone too.
func TestStatusRepairDiffs_WinnerRuleResiduals_NeverAppliedWithoutForce(t *testing.T) {
	ctx := context.Background()
	blocks := func(from, to string) *model.Dependency {
		return &model.Dependency{FromID: from, ToID: to, DepType: model.DepTypeBlocks, CreatedAt: time.Now().UTC()}
	}
	tests := []struct {
		name    string
		setup   func(t *testing.T, s *sqlite.Store, raw *sql.DB)
		flagged string // the node listed as flagged; "" when nothing is listed
	}{
		{"auto-blocked open node, blocker unresolved", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
			mustCreateNode(t, s, "MTIX-2", "")
			require.NoError(t, s.AddDependency(ctx, blocks("MTIX-2", "MTIX-1")))
		}, ""},
		{"foreign invalidation restored locally", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			pullEvents(t, s, []*model.SyncEvent{foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				transition(model.StatusOpen, model.StatusInvalidated), latestLamport(t, raw, model.OpCreateNode)+5, "")})
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "restore", "agent-a"))
			require.NotEqual(t, nullColumn, nodeRow(t, raw, "MTIX-1")["closed_at"], "the restore keeps closed_at")
		}, ""},
		{"cancelled and reopened, then a parent cascade, parent reopened", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			require.NoError(t, s.CancelNode(ctx, "MTIX-1.1", "dropped", "agent-a", false))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1", model.StatusOpen, "reopen", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
		}, "MTIX-1.1"},
		{"auto-blocked after its claim, blocker unresolved", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			mustCreateNode(t, s, "MTIX-2", "")
			require.NoError(t, s.AddDependency(ctx, blocks("MTIX-2", "MTIX-1")))
		}, ""},
		{"descendant cancelled by a cascade", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
		}, ""},
		{"descendant cancelled by a cascade, ancestor reopened", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "reopen", "agent-a"))
		}, ""},
		{"cancelled by a cascade under a done parent", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			mustCreateNode(t, s, "MTIX-1.1.1", "MTIX-1.1")
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1.1", model.StatusDone, "finished", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1.1.1", "agent-a"))
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "dropped", "agent-a", true))
		}, ""},
		{"status and agent_state set by update_field after the claim", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			done, stuck := model.StatusDone, model.AgentStateStuck
			require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Status: &done, AgentState: &stuck}))
		}, ""},
		{"assignee changed after the claim", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			bob := "bob"
			require.NoError(t, s.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Assignee: &bob}))
		}, ""},
		{"own invalidation of an open node", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
		}, ""},
		{"own pushed invalidation of an open node", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
			markEventsPushed(t, raw)
		}, ""},
		{"own restore of an invalidated closed node, then a claim", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "finished", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusInvalidated, "rerun", "agent-a"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusOpen, "restore", "agent-a"))
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-b"))
		}, ""},
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
		}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := replicaWithNode(t)
			tt.setup(t, s, raw)

			diffs, err := s.StatusRepairDiffs(ctx)

			require.NoError(t, err)
			if tt.flagged == "" {
				require.Empty(t, diffs)
				return
			}
			require.Len(t, diffs, 1)
			require.Equal(t, tt.flagged, diffs[0].NodeID)
			require.True(t, diffs[0].Flagged, diffs[0].Reason)
			before := nodeRow(t, raw, tt.flagged)
			_, applied, err := s.RepairNodeStatus(ctx, tt.flagged, "repairer", false)
			require.NoError(t, err)
			require.False(t, applied)
			require.Equal(t, before, nodeRow(t, raw, tt.flagged))
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
// status is that of an older claim, but the stored assignee or agent_state
// is not what that claim wrote, so no older event left this state and the
// node is flagged.
func TestStatusRepairDiffs_StoredAssigneeOfNoOlderClaim_Flagged(t *testing.T) {
	tests := []struct {
		name, assignee, agentState string
	}{
		{"an assignee no claim names", "carol", "working"},
		{"an agent_state no claim wrote", "agent-a", "stuck"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := replicaWithNode(t)
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-a"))
			require.NoError(t, s.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-a"))
			// In progress, written without an event.
			_, err := raw.Exec(`UPDATE nodes SET status = 'in_progress', assignee = ?, agent_state = ?
				WHERE id = 'MTIX-1'`, tt.assignee, tt.agentState)
			require.NoError(t, err)

			diffs, err := s.StatusRepairDiffs(ctx)

			require.NoError(t, err)
			require.Len(t, diffs, 1)
			require.True(t, diffs[0].Flagged)
			require.Equal(t, "not a replay; review: no older event left the stored state", diffs[0].Reason)
		})
	}
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

// TestStatusRepair_AssigneeOnlyDifference_FlaggedAndAppliedOnlyWithForce: a
// difference only in the assignee or agent_state, with the status matching
// the winner, is flagged, never repaired as a replay: mtix update --assignee
// writes no activity, so an imported reassignment cannot be told apart from
// a replayed claim. With force the column is repaired and, the status being
// unchanged, no event is emitted.
func TestStatusRepair_AssigneeOnlyDifference_FlaggedAndAppliedOnlyWithForce(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name  string
		setup func(t *testing.T, a *sqlite.Store, raw *sql.DB)
	}{
		{"a replayed claim by another agent", func(t *testing.T, a *sqlite.Store, raw *sql.DB) {
			require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-x"))
			markEventsPushed(t, raw)
			require.NoError(t, a.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-x"))
			require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-a"))
			replayClaimAsBefore952(t, raw, "MTIX-1", "agent-x")
		}},
		{"an imported mtix update --assignee naming an older claimer", func(t *testing.T, a *sqlite.Store, _ *sql.DB) {
			require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-x"))
			require.NoError(t, a.UnclaimNode(ctx, "MTIX-1", "handoff", "agent-x"))
			require.NoError(t, a.ClaimNode(ctx, "MTIX-1", "agent-a"))
			b, _ := mutationTestStore(t)
			replaceImport(t, b, a)
			agentX := "agent-x"
			require.NoError(t, b.UpdateNode(ctx, "MTIX-1", &store.NodeUpdate{Assignee: &agentX}))
			replaceImport(t, a, b)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, raw := replicaWithNode(t)
			tt.setup(t, a, raw)
			before, events := nodeRow(t, raw, "MTIX-1"), eventCount(t, raw)
			require.Equal(t, "agent-x", before["assignee"])

			diffs, err := a.StatusRepairDiffs(ctx)
			require.NoError(t, err)
			require.Len(t, diffs, 1)
			require.True(t, diffs[0].Flagged)
			require.Equal(t, "not a replay; review: only the assignee or agent_state differs, which a field update can change without a trace",
				diffs[0].Reason)
			require.Equal(t, []sqlite.StatusRepairColumn{
				{Column: "assignee", Current: repairText("agent-x"), Expected: repairText("agent-a")},
			}, diffs[0].Columns)

			_, applied, err := a.RepairNodeStatus(ctx, "MTIX-1", "repairer", false)
			require.NoError(t, err)
			require.False(t, applied)
			require.Equal(t, before, nodeRow(t, raw, "MTIX-1"))

			_, applied, err = a.RepairNodeStatus(ctx, "MTIX-1", "repairer", true)
			require.NoError(t, err)
			require.True(t, applied)
			require.Equal(t, "agent-a", nodeRow(t, raw, "MTIX-1")["assignee"])
			require.Equal(t, events, eventCount(t, raw), "the status did not change, so no event")
		})
	}
}
