// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// snapshotWorktreeOf builds a pushed board, then makes its store what a
// tasks.json auto-import leaves: the nodes and comments are in the store, there
// is no journal row and the pull cursor is where a new store leaves it.
func snapshotWorktreeOf(t *testing.T) (root string, writer *sqlite.Store, hubEvents []*model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	root, writer, hubEvents = worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	_, err := writer.WriteDB().ExecContext(ctx,
		`DELETE FROM sync_events; UPDATE meta SET value = '0' WHERE key = 'meta.sync.last_pulled_clock'; UPDATE meta SET value = '' WHERE key = 'meta.sync.last_pulled_event_id'`)
	require.NoError(t, err)
	return root, writer, hubEvents
}

// TestEnrolWorktree_RealPath_AutoImportedPushedBoardWithAComment_EnrolsWithNoSteps
// is the main case (ruling r5 point 8): a store that never pulled and holds no
// journal row came from a tasks.json snapshot, so its cursor says nothing; it is
// compared with the full hub clone and, being equal, enrols.
func TestEnrolWorktree_RealPath_AutoImportedPushedBoardWithAComment_EnrolsWithNoSteps(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := snapshotWorktreeOf(t)
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	require.NoError(t, err)
	assert.True(t, *recorded)
	clone, err := sqlite.New(worktreeDataDir(root), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = clone.Close() })
	n, err := clone.GetNode(ctx, "TEST-2")
	require.NoError(t, err)
	assert.Len(t, n.Annotations, 1, "the comment is on the clone once")
}

// TestEnrolWorktree_RealPath_AutoImportedStoreLocalEdit_RefusesWithTheLocalSteps:
// the snapshot's title differs from the hub's; the refusal prints the local
// sequence and no pull, and nothing moved.
func TestEnrolWorktree_RealPath_AutoImportedStoreLocalEdit_RefusesWithTheLocalSteps(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := snapshotWorktreeOf(t)
	_, err := writer.WriteDB().ExecContext(ctx, `UPDATE nodes SET title = 'edited in the snapshot' WHERE id = 'TEST-1'`)
	require.NoError(t, err)
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))
	board, storeBefore := fileBytes(t, boardFilePath(root)), treeBytes(t, worktreeDataDir(root))

	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, []string{"title"}, refusal.Diff["TEST-1"])
	assert.NotContains(t, err.Error(), "fix: run mtix sync pull")
	assert.Contains(t, err.Error(), "(1) keep a copy: mtix backup")
	assert.Contains(t, err.Error(), "accept-hub "+refusal.Digest())
	assertNothingChangedExceptReport(t, root, storeBefore, board, recorded)
}

// TestEnrolWorktree_RealPath_AutoImportedStoreMissingHubRows_Enrols: a snapshot
// older than the hub holds nothing the hub lacks, so replacing it loses nothing.
func TestEnrolWorktree_RealPath_AutoImportedStoreMissingHubRows_Enrols(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := snapshotWorktreeOf(t)
	_, err := writer.WriteDB().ExecContext(ctx, `DELETE FROM nodes WHERE id = 'TEST-4'`)
	require.NoError(t, err)
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))

	require.NoError(t, err)
	assert.True(t, *recorded)
}

// TestEnrolWorktree_RealPath_StoreWithAJournalRow_KeepsTheBoundedRule (point 9):
// one journal row ends the snapshot rule, so the bound is the cursor and the
// journal and an unjournaled edit refuses even though the hub holds the node.
func TestEnrolWorktree_RealPath_StoreWithAJournalRow_KeepsTheBoundedRule(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	_, err := writer.WriteDB().ExecContext(ctx, `DELETE FROM meta WHERE key IN ('meta.sync.last_pulled_clock', 'meta.sync.last_pulled_event_id')`)
	require.NoError(t, err)
	_, err = writer.WriteDB().ExecContext(ctx, `UPDATE nodes SET title = 'unjournaled' WHERE id = 'TEST-1'`)
	require.NoError(t, err)
	bound, err := readAppliedBound(ctx, writer)
	require.NoError(t, err)
	assert.False(t, bound.snapshot, "the journal holds rows")
	writeBoardFromStore(t, root, writer)
	require.NoError(t, writer.Close())
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))

	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.False(t, *recorded)
}

func TestReadAppliedBound_SnapshotStore_IsOnlyANeverPulledStoreWithNoJournal(t *testing.T) {
	ctx := context.Background()
	_, writer, _ := snapshotWorktreeOf(t)
	snap, err := readAppliedBound(ctx, writer)
	require.NoError(t, err)
	assert.True(t, snap.snapshot)
	assert.True(t, snap.holds(evAt(99, "any")), "a snapshot store is compared with the full clone")

	_, err = writer.WriteDB().ExecContext(ctx,
		`INSERT OR REPLACE INTO meta (key, value) VALUES ('meta.sync.last_pulled_clock', '5'), ('meta.sync.last_pulled_event_id', 'e')`)
	require.NoError(t, err)
	pulled, err := readAppliedBound(ctx, writer)
	require.NoError(t, err)
	assert.False(t, pulled.snapshot, "a pull ends the snapshot rule")
	assert.False(t, pulled.holds(evAt(99, "any")))
}
