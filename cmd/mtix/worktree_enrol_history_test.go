// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const historyConfig = "history:\n  export: true\n"

// TestEnrolWorktree_RealPath_HistoryExportOn_RefusesThenThePrintedFixEnrols: with
// history.export on, the real tasks.json carries export format 2.1.0, the
// build's version and each node's history. A board written before the switch
// does not equal what the store now exports, so the file rule refuses and the
// printed fix (mtix sync --fix, which writes the file through the real path)
// makes the same worktree enrol. The comparison exports through that same path,
// so the fix can work.
func TestEnrolWorktree_RealPath_HistoryExportOn_RefusesThenThePrintedFixEnrols(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "config.yaml"),
		[]byte("prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"+historyConfig), 0o600))
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))
	board, storeBefore := fileBytes(t, boardFilePath(root)), treeBytes(t, worktreeDataDir(root))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardFileDiffers, refusal.Reason)
	assertNothingChangedExceptReport(t, root, storeBefore, board, recorded)

	runMtixSyncFixWithConfig(t, root, historyConfig)
	assert.Contains(t, string(fileBytes(t, boardFilePath(root))), `"history"`, "the fixed file carries history")

	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))
	if errors.As(err, &refusal) {
		t.Logf("DIFF %v", refusal.Diff)
	}
	require.NoError(t, err)
	assert.True(t, *recorded)
}

// TestEnrolWorktree_RealPath_HistoryExportOn_ExclusionsHideARealDifference: with
// history.export on, the unfiltered comparison of a fully pushed store with its
// bounded clone differs on history (a clone's apply writes its own row ids,
// clock readings and origins), so those are normalized; the rows themselves are
// still compared (see the store-only test below).
func TestEnrolWorktree_RealPath_HistoryExportOn_ExclusionsHideARealDifference(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	require.NoError(t, writer.Close())
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "config.yaml"),
		[]byte("prefix: TEST\nmax_depth: 10\nagent_stale_threshold: 30m\n"+historyConfig), 0o600))
	var local, clone []byte
	require.NoError(t, withScratch(slog.Default(), root, func(scratch string) error {
		return withBoardStore(slog.Default(), root, worktreeDataDir(root), func(st *sqlite.Store) (e error) {
			if local, e = exportBytes(ctx, st); e != nil {
				return e
			}
			clone, e = boundedCloneExport(ctx, slog.Default(), root, scratch, st, hubOf(hubEvents))
			return e
		})
	}))

	raw, err := service.CompareBoardDocsWith(local, clone, nil)
	require.NoError(t, err)
	found := false
	for _, fields := range raw {
		for _, f := range fields {
			found = found || f == "history" || f == "(history)"
		}
	}
	assert.True(t, found, "an unfiltered comparison differs on history: %v", raw)
	filtered, err := service.CompareBoardDocs(local, clone)
	require.NoError(t, err)
	assert.Empty(t, filtered)
}

// TestEnrolWorktree_RealPath_UnreadableConfig_RefusesBeforeAnythingMoves: the
// export follows history.export from the worktree's config, so a config that
// cannot be read cannot be compared against: an error, never a guess.
func TestEnrolWorktree_RealPath_UnreadableConfig_RefusesBeforeAnythingMoves(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := writerWorktreeOnHub(t)
	require.NoError(t, writer.Close())
	require.NoError(t, os.Mkdir(filepath.Join(root, ".mtix", "config.yaml"), 0o700)) // a config that cannot be read
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))
	storeBefore := treeBytes(t, worktreeDataDir(root))

	_, err := svc.EnrolWorktree(ctx, enrolRequest(root))

	require.ErrorContains(t, err, "load the worktree config")
	assertNothingChanged(t, root, storeBefore, nil, recorded)
}

// TestEnrolWorktree_RealPath_HistoryExportOn_StoreOnlyHistoryRows_Refuses is the
// attack on the old rule that dropped history whole: history rows only the
// store holds (what an import merge adds, with no node field changed and no
// journal row) are a difference, and the report names the node's history.
func TestEnrolWorktree_RealPath_HistoryExportOn_StoreOnlyHistoryRows_Refuses(t *testing.T) {
	ctx := context.Background()
	root, writer, hubEvents := worktreeWith(t, func(ctx context.Context, st *sqlite.Store) { pushedWork(ctx, t, st) })
	var uid string
	require.NoError(t, writer.QueryRow(ctx, `SELECT uid FROM nodes WHERE id = 'TEST-1'`).Scan(&uid))
	_, err := writer.WriteDB().ExecContext(ctx,
		`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, new_value) VALUES ('merge-1', ?, '2026-06-03T00:00:00.000000000Z', 'importer', 'local', 'change', 'title', '"merged"')`, uid)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	runMtixSyncFixWithConfig(t, root, historyConfig)
	svc, recorded := enrolOverHub(fakeDial(hubOf(hubEvents)))
	board, storeBefore := fileBytes(t, boardFilePath(root)), treeBytes(t, worktreeDataDir(root))

	_, err = svc.EnrolWorktree(ctx, enrolRequest(root))

	var refusal *service.WorktreeBoardError
	require.ErrorAs(t, err, &refusal)
	assert.Equal(t, service.BoardHubDiffers, refusal.Reason)
	assert.Equal(t, []string{"history"}, refusal.Diff["TEST-1"])
	assert.NotContains(t, err.Error(), "fix: run mtix sync pull")
	assertNothingChangedExceptReport(t, root, storeBefore, board, recorded)
}
