// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

// initStoreAt creates a real store under dir/.mtix/data and returns its instance id.
func initStoreAt(t *testing.T, dir string) string {
	t.Helper()
	dataDir := filepath.Join(dir, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	store, err := sqlite.New(dataDir, slog.Default())
	require.NoError(t, err)
	id, err := store.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	return id
}

func listAt(t *testing.T, st *oplocal.State, dir, id string) {
	t.Helper()
	require.NoError(t, st.ListBoardInstance(t.Context(), oplocal.ListedBoard{Key: id, Path: dir, StoreInstanceID: id}))
}

// The glue judges the store the verb would open: a store nested under a listed
// checkout, or committed on a worktree branch, is judged as itself.
func TestGlue_ProductionPredicate_NestedStoreUnderListedCheckout_Declines(t *testing.T) {
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	nested := filepath.Join(root, "vend", "x")
	initStoreAt(t, nested)
	t.Chdir(nested)
	got := productionGluePredicate(t.Context())
	assert.False(t, got.Act)
	assert.Equal(t, attach.ReasonUnlisted, got.Reason)
}

func TestGlue_ProductionPredicate_UnlistedWorktreeStoreOfListedMain_Declines(t *testing.T) {
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	wt := linkedWorktreeOf(t, root)
	initStoreAt(t, wt) // a store committed on the worktree's branch
	t.Chdir(wt)
	got := productionGluePredicate(t.Context())
	assert.False(t, got.Act, "the worktree's own store is not the listed main's")
	assert.Equal(t, attach.ReasonUnlisted, got.Reason)
}

func TestGlue_ProductionPredicate_SubdirectoryBoard_ActsWhenListed(t *testing.T) {
	st := testutil.OperatorState(t)
	repo := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(repo, ".git"), 0o755))
	board := filepath.Join(repo, "pkg")
	id := initStoreAt(t, board)
	listAt(t, st, board, id)
	sub := filepath.Join(board, "src")
	require.NoError(t, os.MkdirAll(sub, 0o755))
	t.Chdir(sub)
	got := productionGluePredicate(t.Context())
	require.True(t, got.Act, got.Reason)
	assert.Equal(t, board, got.BoardRoot)
}

// ctxAdapter records the context the verb handed the adapter.
type ctxAdapter struct{ roots []string }

func (a *ctxAdapter) Run(ctx context.Context, _ attach.VerbRequest) error {
	root, _ := attach.StoreRootFrom(ctx)
	a.roots = append(a.roots, root)
	return nil
}

func TestAttachVerb_AdapterContext_CarriesTheStoreRootThePredicateResolved(t *testing.T) {
	st := testutil.OperatorState(t)
	adapter := &ctxAdapter{}
	deps := attachTestDeps(st, 2, map[string]attach.Adapter{"claude-code": adapter})
	deps.glue = func(context.Context) attach.GlueDecision {
		return attach.GlueDecision{Act: true, BoardRoot: "/resolved/board", StoreRoot: "/resolved/board"}
	}
	_, _, err := runAttachVerbForTest(t, deps, "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Equal(t, []string{"/resolved/board"}, adapter.roots)
}

// The pi adapter opens the store the predicate resolved and no other path, even
// when the working directory sits in a different board.
func TestOpenPiWaitBoard_OpensOnlyTheResolvedStore(t *testing.T) {
	resolved, elsewhere := t.TempDir(), t.TempDir()
	want := initStoreAt(t, resolved)
	other := initStoreAt(t, elsewhere)
	require.NotEqual(t, want, other)
	t.Chdir(elsewhere)

	opened, closeStore, err := openPiWaitBoard(attach.WithStoreRoot(t.Context(), resolved))
	require.NoError(t, err)
	defer func() { require.NoError(t, closeStore()) }()
	store, ok := opened.(interface {
		StoreInstanceID(context.Context) (string, error)
	})
	require.True(t, ok)
	got, err := store.StoreInstanceID(t.Context())
	require.NoError(t, err)
	assert.Equal(t, want, got, "the wait read the resolved board, not the one under the working directory")
}

func TestOpenPiWaitBoard_NoResolvedStore_FailsWithoutSearching(t *testing.T) {
	elsewhere := t.TempDir()
	initStoreAt(t, elsewhere)
	t.Chdir(elsewhere)
	opened, closeStore, err := openPiWaitBoard(t.Context())
	require.Error(t, err, "a board under the working directory is not a substitute for the resolved one")
	assert.Nil(t, opened)
	assert.Nil(t, closeStore)
}

// legacyBoardCheckout lists a git checkout whose store has no instance id.
func legacyBoardCheckout(t *testing.T, st *oplocal.State) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	dataDir := filepath.Join(root, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	store, err := sqlite.New(dataDir, slog.Default())
	require.NoError(t, err)
	_, err = store.WriteDB().ExecContext(t.Context(), "DELETE FROM meta WHERE key='store_instance_id'")
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, st.ListBoard(t.Context(), "11111111-1111-4111-8111-111111111111", root))
	return root
}

// A legacy board (no instance id) has no routing identity: its queue is left
// alone, with no drain and no alert.
func TestGlue_ListedLegacyBoard_SessionStartLeavesQueueAlone(t *testing.T) {
	st := testutil.OperatorState(t)
	root := legacyBoardCheckout(t, st)
	boards, err := st.DispatchBoards(t.Context())
	require.NoError(t, err)
	enqueueRow(t, root, forgedRow(t, boards.Listed[0].Key, t.TempDir()))
	t.Chdir(root)
	adapter := &recordingAdapter{}

	_, stderr, err := runAttachVerbForTest(t, productionGlueDeps(st, adapter), "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Len(t, adapter.calls, 1, "a legacy board is attached and the verb runs")
	assert.Len(t, pendingRows(t, root), 1, "a legacy store has nothing to drain")
	assert.Empty(t, attachAlerts(t, st))
}

// session-start records activity for the store the predicate resolved, even
// when the working directory is elsewhere; the wait verb records nothing.
func TestAttachVerb_RecordsGlueActivityOnTheResolvedStore(t *testing.T) {
	src := hubSourceFixture(t, "")
	root := filepath.Dir(src.mtixDir)
	t.Chdir(t.TempDir())
	deps := attachTestDeps(src.state, 2, map[string]attach.Adapter{"claude-code": &recordingAdapter{}})
	deps.glue = func(context.Context) attach.GlueDecision {
		return attach.GlueDecision{Act: true, BoardRoot: root, StoreRoot: root}
	}
	_, _, err := runAttachVerbForTest(t, deps, "wait", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	_, present, err := src.state.ReadHubActivity(src.key())
	require.NoError(t, err)
	require.False(t, present, "the wait verb is not activity")

	_, _, err = runAttachVerbForTest(t, deps, "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	got, present, err := src.state.ReadHubActivity(src.key())
	require.NoError(t, err)
	require.True(t, present, "session-start must record activity for the resolved store")
	assert.Equal(t, oplocal.ActivityGlue, got.Kind)
}

// A working directory that cannot be read means there is nothing to judge: the
// predicate declines instead of acting.
func TestGlue_ProductionPredicate_UnreadableWorkingDirectory_Declines(t *testing.T) {
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	gone := filepath.Join(root, "gone")
	require.NoError(t, os.Mkdir(gone, 0o755))
	t.Chdir(gone)
	require.NoError(t, os.Remove(gone))
	got := productionGluePredicate(t.Context())
	assert.False(t, got.Act)
	assert.Equal(t, attach.ReasonNoCheckout, got.Reason)
}

// The wait opens a store only if its data directory is there: sqlite.New would
// create one, and the wait must never create a board.
func TestOpenPiWaitBoard_MissingDataDirectory_FailsAndCreatesNothing(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".mtix"), 0o755))
	opened, closeStore, err := openPiWaitBoard(attach.WithStoreRoot(t.Context(), root))
	require.Error(t, err)
	assert.Nil(t, opened)
	assert.Nil(t, closeStore)
	_, statErr := os.Stat(filepath.Join(root, ".mtix", "data"))
	assert.True(t, os.IsNotExist(statErr), "no data directory may appear")
}
