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
	store, ok := opened.(*sqlite.Store)
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
