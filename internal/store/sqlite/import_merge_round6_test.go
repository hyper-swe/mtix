// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-112.17.16.16 round 6, principle H: on a store that syncs a
// merge changes no dependency on a task the sync log holds and sets no status
// that dependencies decide; a file dependency it does not apply is named with
// the mtix dep command. A pull that meets a local task on a number the hub
// gives to another task moves the local task instead of dropping the hub's.
package sqlite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// trio returns a synced importer holding REC-1..REC-n and two peers that
// applied its creates in the same pull: one stands for an incremental peer,
// one for a fresh clone.
func trio(t *testing.T, n int) (importer, peer, fresh *sqlite.Store) {
	t.Helper()
	importer, peer, fresh = newTestStore(t), newTestStore(t), newTestStore(t)
	for i := 1; i <= n; i++ {
		createPlainTask(t, importer, fmt.Sprintf("REC-%d", i), i, "", sameIDTime)
	}
	replicateToAll(t, importer, peer, fresh)
	return importer, peer, fresh
}

func statusAll(t *testing.T, id string, stores map[string]*sqlite.Store) map[string]string {
	t.Helper()
	out := map[string]string{}
	for who, s := range stores {
		out[who] = viewOf(t, s, id).Status
	}
	return out
}

// TestImportMerge_LinkAndItsResolvedBlockerInOneFile_EveryReplicaAgrees (round
// 5 S1): a file that links a blocker to a held task and resolves the blocker
// leaves the dependent as it was on the importer, the peer and a fresh clone.
func TestImportMerge_LinkAndItsResolvedBlockerInOneFile_EveryReplicaAgrees(t *testing.T) {
	for _, blocker := range []string{"done", "cancelled", "invalidated"} {
		t.Run(blocker, func(t *testing.T) {
			importer, peer, fresh := trio(t, 2)
			data := exportOf(t, importer)
			data.Nodes[fileIdx(t, data, "REC-2")].Status = blocker
			addFileDep(t, data, "REC-2", "REC-1", "blocks")

			res := mergeTheirs(t, importer, data)
			replicateToAll(t, importer, peer, fresh)

			all := map[string]*sqlite.Store{"importer": importer, "peer": peer, "fresh": fresh}
			for who, status := range statusAll(t, "REC-1", all) {
				require.Equal(t, "open", status, who)
			}
			for who, status := range statusAll(t, "REC-2", all) {
				require.Equal(t, blocker, status, who)
			}
			require.Equal(t, []string{"mtix dep add REC-2 REC-1 --type blocks"}, res.SkippedDeps)
			require.Zero(t, res.DepsImported)
			require.Zero(t, countJournal(t, importer, `op_type = 'link_dep'`))
		})
	}
}

// TestImportMerge_DependentLeftBlockedByTheFile_IsReleasedByTheBlockerRuleEverywhere:
// the file resolves a blocker and leaves the blocked dependent alone: the
// importer releases it by the shared rule, as the journaled transition does
// on a peer.
func TestImportMerge_DependentLeftBlockedByTheFile_IsReleasedByTheBlockerRuleEverywhere(t *testing.T) {
	importer, peer, fresh := trio(t, 2)
	require.NoError(t, importer.AddDependency(context.Background(),
		&model.Dependency{FromID: "REC-2", ToID: "REC-1", DepType: model.DepTypeBlocks}))
	replicateToAll(t, importer, peer, fresh)
	data := exportOf(t, importer)
	data.Nodes[fileIdx(t, data, "REC-2")].Status = string(model.StatusDone)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)
	replicateToAll(t, importer, peer, fresh)

	for who, status := range statusAll(t, "REC-1", map[string]*sqlite.Store{"importer": importer, "peer": peer, "fresh": fresh}) {
		require.Equal(t, "open", status, who)
	}
}

// TestImportMerge_DependentGivenAStatusByTheFile_KeepsItEverywhere (round 5
// S1): the dependent was open before the block, the file says in_progress and
// resolves the blocker; no replica restores another status.
func TestImportMerge_DependentGivenAStatusByTheFile_KeepsItEverywhere(t *testing.T) {
	for _, tc := range []struct{ name, blocker string }{{"blocker done", "done"}, {"blocker still open", "open"}} {
		t.Run(tc.name, func(t *testing.T) {
			importer, peer, fresh := trio(t, 2)
			require.NoError(t, importer.AddDependency(context.Background(),
				&model.Dependency{FromID: "REC-2", ToID: "REC-1", DepType: model.DepTypeBlocks}))
			replicateToAll(t, importer, peer, fresh)
			data := exportOf(t, importer)
			data.Nodes[fileIdx(t, data, "REC-1")].Status = string(model.StatusInProgress)
			data.Nodes[fileIdx(t, data, "REC-2")].Status = tc.blocker
			require.NoError(t, sqlite.RecomputeExportChecksum(data))

			mergeTheirs(t, importer, data)
			replicateToAll(t, importer, peer, fresh)

			for who, s := range map[string]*sqlite.Store{"importer": importer, "peer": peer, "fresh": fresh} {
				require.Equal(t, "in_progress", viewOf(t, s, "REC-1").Status, who)
			}
		})
	}
}

// TestImportMerge_DependencyOnHeldTasks_IsNotAppliedNamedAndNamedAgainOnARepeat:
// whichever end is held, the file's link is not inserted, not counted, not
// journaled, and named with the command that adds it.
func TestImportMerge_DependencyOnHeldTasks_IsNotAppliedNamedAndNamedAgainOnARepeat(t *testing.T) {
	for _, tc := range []struct{ name, from, to, typ string }{
		{"blocks", "REC-2", "REC-1", "blocks"},
		{"related", "REC-1", "REC-2", "related"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			importer, _, _ := trio(t, 2)
			data := exportOf(t, importer)
			addFileDep(t, data, tc.from, tc.to, tc.typ)
			want := []string{fmt.Sprintf("mtix dep add %s %s --type %s", tc.from, tc.to, tc.typ)}

			first := mergeTheirs(t, importer, data)
			again := mergeTheirs(t, importer, data)

			require.Equal(t, want, first.SkippedDeps)
			require.Equal(t, want, again.SkippedDeps)
			require.Zero(t, first.DepsImported)
			var held int
			require.NoError(t, importer.WriteDB().QueryRow(`SELECT COUNT(*) FROM dependencies`).Scan(&held))
			require.Zero(t, held, "the link is not inserted")
			require.Zero(t, countJournal(t, importer, `op_type = 'link_dep' AND sync_status = 'pending'`))
			require.Equal(t, "open", viewOf(t, importer, "REC-1").Status, "nothing blocked it")
		})
	}
}

// TestImportMerge_DependencyBetweenTasksTheLogLacks_IsAppliedLocallyAndUnsent:
// a link between two new tasks is the file's own and is kept as written,
// with the status the file gave; nothing is journaled for it.
func TestImportMerge_DependencyBetweenTasksTheLogLacks_IsAppliedLocallyAndUnsent(t *testing.T) {
	importer, _, _ := trio(t, 1)
	bob := newTestStore(t)
	createPlainTask(t, bob, "REC-2", 2, "", sameIDTime)
	createPlainTask(t, bob, "REC-3", 3, "", sameIDTime)
	require.NoError(t, bob.AddDependency(context.Background(),
		&model.Dependency{FromID: "REC-2", ToID: "REC-3", DepType: model.DepTypeBlocks}))
	data := exportOf(t, bob)
	data.Nodes[fileIdx(t, data, "REC-3")].Status = string(model.StatusOpen)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)

	require.Equal(t, 1, res.DepsImported)
	require.Empty(t, res.SkippedDeps)
	require.ElementsMatch(t, []string{"REC-2", "REC-3"}, res.UnsentTasks)
	require.Equal(t, "open", viewOf(t, importer, "REC-3").Status, "the file's status stands; nothing blocked it")
	require.Zero(t, countJournal(t, importer, `sync_status = 'pending'`))
}

// TestImportMerge_FileStatusOverABlockedTask_IsWhatEveryReplicaHolds: the
// merge no longer keeps a blocked task blocked against the file; it journals
// the transition, so the replicas hold the file's status, whatever type of
// link the task has.
func TestImportMerge_FileStatusOverABlockedTask_IsWhatEveryReplicaHolds(t *testing.T) {
	for _, typ := range []model.DepType{model.DepTypeBlocks, model.DepTypeRelated} {
		t.Run(string(typ), func(t *testing.T) {
			importer, peer, fresh := trio(t, 2)
			require.NoError(t, importer.AddDependency(context.Background(),
				&model.Dependency{FromID: "REC-2", ToID: "REC-1", DepType: typ}))
			replicateToAll(t, importer, peer, fresh)
			for _, s := range []*sqlite.Store{importer, peer, fresh} {
				_, err := s.WriteDB().Exec(`UPDATE nodes SET status = 'blocked', previous_status = 'open' WHERE id = 'REC-1'`)
				require.NoError(t, err)
			}
			data := exportOf(t, importer)
			data.Nodes[fileIdx(t, data, "REC-1")].Status = string(model.StatusOpen)
			require.NoError(t, sqlite.RecomputeExportChecksum(data))

			mergeTheirs(t, importer, data)
			replicateToAll(t, importer, peer, fresh)

			for who, s := range map[string]*sqlite.Store{"importer": importer, "peer": peer, "fresh": fresh} {
				require.Equal(t, "open", viewOf(t, s, "REC-1").Status, who)
			}
		})
	}
}

// TestPull_CreateOnANumberHeldByATaskWithNoCreate_MovesTheLocalTaskAndKeepsBoth
// (round 5 S1, ruling 2): Bob's task was merged here with no create in the
// log; the hub's task for the same number arrives. The local task moves to the
// next free number, with its uid and title; the hub's task takes the number.
func TestPull_CreateOnANumberHeldByATaskWithNoCreate_MovesTheLocalTaskAndKeepsBoth(t *testing.T) {
	alice, _, _ := trio(t, 1)
	bob, carol := newTestStore(t), newTestStore(t)
	createSameIDTask(t, bob, "REC-2", "", 2, taskUID(t), "Bob task")
	file := exportOf(t, bob)
	mergeTheirs(t, alice, file)
	var bobUID string
	require.NoError(t, alice.WriteDB().QueryRow(`SELECT uid FROM nodes WHERE id = 'REC-2'`).Scan(&bobUID))
	createSameIDTask(t, carol, "REC-2", "", 2, backfilledUID(t), "Carol task")

	replicate(t, carol, alice)

	require.Equal(t, "Carol task", titleOf(t, alice, "REC-2"), "the hub's task takes the number")
	var movedTo, uid string
	require.NoError(t, alice.WriteDB().QueryRow(`SELECT id, uid FROM nodes WHERE title = 'Bob task'`).Scan(&movedTo, &uid))
	require.NotEqual(t, "REC-2", movedTo, "the local task moved")
	require.Equal(t, bobUID, uid, "it keeps its uid")
	require.Zero(t, countJournal(t, alice, `sync_status = 'pending'`), "the move journals nothing")
}

// TestIngestsRelay_ReadsTheIngestCursor: the one predicate the backfill
// refusal and the import's no-route line share is true exactly when the store
// holds a relay ingest cursor (round 5 S3).
func TestIngestsRelay_ReadsTheIngestCursor(t *testing.T) {
	s := newTestStore(t)
	before, err := s.IngestsRelay(context.Background())
	require.NoError(t, err)
	require.False(t, before)

	_, err = s.WriteDB().Exec(`INSERT INTO relay_ingest_cursor (peer_id, segment_no, rs, pub_epoch) VALUES ('0123456789abcdef', 1, 0, 0)`)
	require.NoError(t, err)

	after, err := s.IngestsRelay(context.Background())
	require.NoError(t, err)
	require.True(t, after)
}
