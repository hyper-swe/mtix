// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for the dependency settling rules of a merge (MTIX-112.17.16.16
// round 6): a dependency the store holds, one with a missing end, and the
// unsent naming of both ends of a dependency between tasks the log lacks.
package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestImportMerge_DependencyTheStoreHoldsOnHeldTasks_IsNeitherNamedNorChanged:
// a file that repeats a link the store holds changes nothing, so the merge
// does not tell the person to add it again.
func TestImportMerge_DependencyTheStoreHoldsOnHeldTasks_IsNeitherNamedNorChanged(t *testing.T) {
	importer, _, _ := trio(t, 2)
	require.NoError(t, importer.AddDependency(context.Background(),
		&model.Dependency{FromID: "REC-2", ToID: "REC-1", DepType: model.DepTypeBlocks}))
	data := exportOf(t, importer)

	res := mergeTheirs(t, importer, data)

	require.Empty(t, res.SkippedDeps)
}

// TestImportMerge_DependencyWithOneMissingEndAndOneHeld_IsNotNamed: a link to
// a task that exists nowhere fails at the insert as before; it is not
// presented as a command the person can run.
func TestImportMerge_DependencyWithOneMissingEndAndOneHeld_IsNotNamed(t *testing.T) {
	for _, missingTo := range []bool{true, false} {
		importer, _, _ := trio(t, 1)
		data := exportOf(t, importer)
		if missingTo {
			addFileDep(t, data, "REC-1", "REC-99", "blocks")
		} else {
			addFileDep(t, data, "REC-99", "REC-1", "blocks")
		}

		_, res, err := importer.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{
			Mode: sqlite.ImportModeMerge, Workflow: sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs}})

		if err == nil {
			require.Empty(t, res.SkippedDeps)
		}
		require.Zero(t, countJournal(t, importer, `op_type = 'link_dep'`))
	}
}

// TestImportMerge_DependencyBetweenTwoLocalOnlyTasks_NamesBothEndsUnsent: the
// link is applied as the file wrote it, and both tasks, which no event
// carries, are named unsent.
func TestImportMerge_DependencyBetweenTwoLocalOnlyTasks_NamesBothEndsUnsent(t *testing.T) {
	importer, _, _ := trio(t, 1)
	bob := newTestStore(t)
	createPlainTask(t, bob, "REC-5", 5, "", sameIDTime)
	createPlainTask(t, bob, "REC-6", 6, "", sameIDTime)
	mergeTheirs(t, importer, exportOf(t, bob)) // both arrive; the log holds neither
	data := exportOf(t, importer)
	addFileDep(t, data, "REC-5", "REC-6", "related")

	res := mergeTheirs(t, importer, data)

	require.Equal(t, 1, res.DepsImported)
	require.ElementsMatch(t, []string{"REC-5", "REC-6"}, res.UnsentTasks)
}

// blockedPair returns a synced trio where REC-1 blocks REC-2 and REC-2 is
// blocked, remembering previous, on every replica.
func blockedPair(t *testing.T, previous string) (importer, peer, fresh *sqlite.Store) {
	t.Helper()
	importer, peer, fresh = trio(t, 2)
	require.NoError(t, importer.AddDependency(context.Background(),
		&model.Dependency{FromID: "REC-1", ToID: "REC-2", DepType: model.DepTypeBlocks}))
	replicateToAll(t, importer, peer, fresh)
	for _, s := range []*sqlite.Store{importer, peer, fresh} {
		_, err := s.WriteDB().Exec(`UPDATE nodes SET status = 'blocked', previous_status = ? WHERE id = 'REC-2'`, previous)
		require.NoError(t, err)
	}
	return importer, peer, fresh
}

func previousStatusOf(t *testing.T, s *sqlite.Store, id string) string {
	t.Helper()
	var v string
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COALESCE(previous_status, '') FROM nodes WHERE id = ?`, id).Scan(&v))
	return v
}

// TestImportMerge_BlockerResolvedByTheFile_ReleasesTheDependentAndClearsItsMemory:
// the release restores the status the dependent held before it was blocked,
// forgets that status as a peer does, and does not call the clear a local-only
// change.
func TestImportMerge_BlockerResolvedByTheFile_ReleasesTheDependentAndClearsItsMemory(t *testing.T) {
	importer, peer, fresh := blockedPair(t, "in_progress")
	data := exportOf(t, importer)
	data.Nodes[fileIdx(t, data, "REC-1")].Status = "done"
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	res := mergeTheirs(t, importer, data)
	replicateToAll(t, importer, peer, fresh)

	for who, s := range map[string]*sqlite.Store{"importer": importer, "peer": peer, "fresh": fresh} {
		require.Equal(t, "in_progress", viewOf(t, s, "REC-2").Status, who)
	}
	require.Empty(t, previousStatusOf(t, importer, "REC-2"), "the remembered status is cleared")
	require.NotContains(t, fieldsNamed(res, "REC-2"), "previous_status", "a peer clears it too, so it is not local-only")
}

// TestImportMerge_ChangeToABlockerThatWasAlreadyResolved_ReleasesNothing: a
// blocker whose status the file does not change is not a transition, so a
// dependent that is still blocked stays as it is, as on a peer.
func TestImportMerge_ChangeToABlockerThatWasAlreadyResolved_ReleasesNothing(t *testing.T) {
	importer, _, _ := blockedPair(t, "open")
	_, err := importer.WriteDB().Exec(`UPDATE nodes SET status = 'done' WHERE id = 'REC-1'`)
	require.NoError(t, err)
	data := exportOf(t, importer)
	data.Nodes[fileIdx(t, data, "REC-1")].Title = "Retitled blocker"
	data.Nodes[fileIdx(t, data, "REC-1")].ContentHash = plainHash("Retitled blocker")
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)

	require.Equal(t, "Retitled blocker", viewOf(t, importer, "REC-1").Title)
	require.Equal(t, "blocked", viewOf(t, importer, "REC-2").Status)
}

// TestPull_CreateOnANumberHeldByATaskWithNoUid_DoesNotMoveIt: a local task
// that carries no uid cannot be told apart from the hub's, so the pull leaves
// it where it is and the hub's task does not take its number.
func TestPull_CreateOnANumberHeldByATaskWithNoUid_DoesNotMoveIt(t *testing.T) {
	alice := newTestStore(t)
	carol := newTestStore(t)
	createSameIDTask(t, alice, "REC-2", "", 2, taskUID(t), "Alice task")
	_, err := alice.WriteDB().Exec(`UPDATE nodes SET uid = '' WHERE id = 'REC-2'`)
	require.NoError(t, err)
	createSameIDTask(t, carol, "REC-2", "", 2, backfilledUID(t), "Carol task")
	events, err := carol.ReadPendingEvents(context.Background(), 100)
	require.NoError(t, err)

	for _, e := range events {
		_ = alice.WithTx(context.Background(), func(tx *sql.Tx) error { return sqlite.IdempotentApply(context.Background(), tx, e) })
	}

	require.Equal(t, "Alice task", titleOf(t, alice, "REC-2"))
	var n int
	require.NoError(t, alice.WriteDB().QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&n))
	require.Equal(t, 1, n, "nothing moved and nothing was added")
}

// TestPull_CreateOnANumberHeldByATaskTheLogCreated_DoesNotMoveIt: a local task
// whose creation the sync log holds is not moved off its number by a pull; the
// number conflict follows its own route, and the task stays where it is.
func TestPull_CreateOnANumberHeldByATaskTheLogCreated_DoesNotMoveIt(t *testing.T) {
	alice, carol := newTestStore(t), newTestStore(t)
	createSameIDTask(t, alice, "REC-2", "", 2, taskUID(t), "Alice task")
	createSameIDTask(t, carol, "REC-2", "", 2, backfilledUID(t), "Carol task")
	require.Positive(t, countJournal(t, alice, `op_type = 'create_node'`), "the log holds Alice's creation")
	events, err := carol.ReadPendingEvents(context.Background(), 100)
	require.NoError(t, err)

	for _, e := range events {
		_ = alice.WithTx(context.Background(), func(tx *sql.Tx) error { return sqlite.IdempotentApply(context.Background(), tx, e) })
	}

	require.Equal(t, "Alice task", titleOf(t, alice, "REC-2"))
	var n int
	require.NoError(t, alice.WriteDB().QueryRow(`SELECT COUNT(*) FROM nodes`).Scan(&n))
	require.Equal(t, 1, n, "nothing moved")
}

// TestImportMerge_DependencyOfAnInvalidTypeOnHeldTasks_IsNotNamed: a type the
// model does not know fails at the insert as before; it is not presented as a
// command the person can run.
func TestImportMerge_DependencyOfAnInvalidTypeOnHeldTasks_IsNotNamed(t *testing.T) {
	importer, _, _ := trio(t, 2)
	data := exportOf(t, importer)
	addFileDep(t, data, "REC-2", "REC-1", "bogus")

	_, res, err := importer.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Workflow: sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs}})

	if err == nil {
		require.Empty(t, res.SkippedDeps)
	}
}

// TestImportMerge_BlockerTheLogLacksResolvedByTheFile_ReleasesNothing: no
// event carries the blocker's transition, so a peer cannot derive a release
// and the merge does not make one.
func TestImportMerge_BlockerTheLogLacksResolvedByTheFile_ReleasesNothing(t *testing.T) {
	importer, _, _ := trio(t, 1)
	bob := newTestStore(t)
	createPlainTask(t, bob, "REC-5", 5, "", sameIDTime)
	createPlainTask(t, bob, "REC-6", 6, "", sameIDTime)
	require.NoError(t, bob.AddDependency(context.Background(),
		&model.Dependency{FromID: "REC-5", ToID: "REC-6", DepType: model.DepTypeBlocks}))
	mergeTheirs(t, importer, exportOf(t, bob))
	require.Equal(t, "blocked", viewOf(t, importer, "REC-6").Status)
	data := exportOf(t, importer)
	data.Nodes[fileIdx(t, data, "REC-5")].Status = "done"
	require.NoError(t, sqlite.RecomputeExportChecksum(data))

	mergeTheirs(t, importer, data)

	require.Equal(t, "done", viewOf(t, importer, "REC-5").Status)
	require.Equal(t, "blocked", viewOf(t, importer, "REC-6").Status)
}
