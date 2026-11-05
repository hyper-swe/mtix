// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
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

func notAttachedGlue(context.Context) attach.GlueDecision {
	return attach.GlueDecision{Reason: attach.ReasonUnlisted, Line: attach.NotAttachedLine}
}

func TestGlue_UnlistedDir_VerbPrintsNotAttachedAndTouchesNothing(t *testing.T) {
	st := testutil.OperatorState(t)
	adapter := &recordingAdapter{}
	deps := attachTestDeps(st, 2, map[string]attach.Adapter{"claude-code": adapter})
	deps.glue = notAttachedGlue

	// Every verb, a stale protocol and an unknown harness would each write an
	// alert or a refresh mark on an attached board; here none may.
	for _, args := range [][]string{
		{"session-start", "--harness", "claude-code", "--protocol", "2"},
		{"session-start", "--harness", "claude-code", "--protocol", "1"},
		{"session-start", "--harness", "claude-code", "--protocol", "99"},
		{"session-start", "--harness", "no-such-harness", "--protocol", "2"},
	} {
		stdout, stderr, err := runAttachVerbForTest(t, deps, args...)
		require.NoError(t, err)
		assert.Equal(t, "board not attached on this host\n", stderr, args)
		assert.Empty(t, stdout)
	}
	assert.Empty(t, adapter.calls, "no adapter, so no injection or proposal")
	assert.Empty(t, attachAlerts(t, st), "no oplocal alert write")
	pending, err := attach.PendingGlueRefresh(st)
	require.NoError(t, err)
	assert.Empty(t, pending, "no glue refresh mark")
}

func TestGlue_AttachedDir_VerbRunsAdapter(t *testing.T) {
	st := testutil.OperatorState(t)
	adapter := &recordingAdapter{}
	deps := attachTestDeps(st, 2, map[string]attach.Adapter{"claude-code": adapter})
	_, stderr, err := runAttachVerbForTest(t, deps, "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Len(t, adapter.calls, 1)
}

func TestGlue_NoPredicateWired_FailsClosed(t *testing.T) {
	st := testutil.OperatorState(t)
	adapter := &recordingAdapter{}
	deps := attachTestDeps(st, 2, map[string]attach.Adapter{"claude-code": adapter})
	deps.glue = nil
	_, stderr, err := runAttachVerbForTest(t, deps, "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Equal(t, "board not attached on this host\n", stderr)
	assert.Empty(t, adapter.calls)
}

func TestGlue_ProductionDeps_WirePredicate(t *testing.T) {
	assert.NotNil(t, productionAttachDeps().glue, "the production glue entry must carry the predicate")
	assert.NotNil(t, productionAttachDeps().drain, "the production glue entry must carry the queue drain")
}

// listedBoardCheckout makes a git checkout with a real store and lists it in st,
// the way a host that attached the board would.
func listedBoardCheckout(t *testing.T, st *oplocal.State) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	dataDir := filepath.Join(root, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	store, err := sqlite.New(dataDir, slog.Default())
	require.NoError(t, err)
	id, err := store.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, st.ListBoardInstance(t.Context(), oplocal.ListedBoard{Key: id, Path: root, StoreInstanceID: id}))
	return root
}

func TestGlue_ProductionPredicate_ListedCheckoutActsUnlistedDoesNot(t *testing.T) {
	st := testutil.OperatorState(t)
	t.Chdir(listedBoardCheckout(t, st))
	got := productionGluePredicate(t.Context())
	require.True(t, got.Act, got.Reason)

	other := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(other, ".git"), 0o755))
	t.Chdir(other)
	got = productionGluePredicate(t.Context())
	assert.False(t, got.Act)
	assert.Equal(t, attach.ReasonUnlisted, got.Reason)
}

// productionGlueDeps wires the real predicate and drain into test deps.
func productionGlueDeps(st *oplocal.State, adapter attach.Adapter) attachDeps {
	deps := attachTestDeps(st, 2, map[string]attach.Adapter{"claude-code": adapter})
	deps.glue = productionGluePredicate
	deps.drain = productionGlueDrain
	return deps
}

// enqueueRow plants an attach_pending row in the store under root.
func enqueueRow(t *testing.T, root string, row sqlite.AttachPending) {
	t.Helper()
	store, err := sqlite.New(filepath.Join(root, ".mtix", "data"), slog.Default())
	require.NoError(t, err)
	_, err = store.EnqueueAttachPending(t.Context(), row)
	require.NoError(t, err)
	require.NoError(t, store.Close())
}

func pendingRows(t *testing.T, root string) []sqlite.AttachPending {
	t.Helper()
	store, err := sqlite.New(filepath.Join(root, ".mtix", "data"), slog.Default())
	require.NoError(t, err)
	rows, err := store.ListAttachPending(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	return rows
}

func forgedRow(t *testing.T, key, other string) sqlite.AttachPending {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"agent_id": "forged", "harness": "codex", "lane_path": other})
	require.NoError(t, err)
	return sqlite.AttachPending{Kind: "lane", BoardKey: key, Path: other, Payload: string(payload), QueuedAt: "now"}
}

func TestGlue_AttachedBoard_SessionStartDrainsQueueAndDropsForgedRow(t *testing.T) {
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	boards, err := st.DispatchBoards(t.Context())
	require.NoError(t, err)
	key := boards.Listed[0].Key
	enqueueRow(t, root, forgedRow(t, key, t.TempDir()))
	t.Chdir(root)
	adapter := &recordingAdapter{}

	_, stderr, err := runAttachVerbForTest(t, productionGlueDeps(st, adapter), "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Empty(t, pendingRows(t, root), "the forged row is dropped, not left queued")
	alerts := attachAlerts(t, st)
	require.Len(t, alerts, 1)
	assert.Equal(t, "attach.pending-rejected", alerts[0].Class)
	assert.Len(t, adapter.calls, 1, "the verb still runs after the drain")
}

func TestGlue_UnlistedBoard_LeavesPendingQueueUntouched(t *testing.T) {
	st := testutil.OperatorState(t)
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o755))
	dataDir := filepath.Join(root, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	store, err := sqlite.New(dataDir, slog.Default())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	enqueueRow(t, root, forgedRow(t, "00000000-0000-4000-8000-000000000000", t.TempDir()))
	t.Chdir(root)
	adapter := &recordingAdapter{}

	_, stderr, err := runAttachVerbForTest(t, productionGlueDeps(st, adapter), "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Equal(t, "board not attached on this host\n", stderr)
	assert.Len(t, pendingRows(t, root), 1, "an unlisted board's queue is not read, drained or dropped")
	assert.Empty(t, attachAlerts(t, st))
	assert.Empty(t, adapter.calls)
}

func TestGlue_OnlySessionStartDrains(t *testing.T) {
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	boards, err := st.DispatchBoards(t.Context())
	require.NoError(t, err)
	enqueueRow(t, root, forgedRow(t, boards.Listed[0].Key, t.TempDir()))
	t.Chdir(root)
	deps := productionGlueDeps(st, &recordingAdapter{})
	_, _, err = runAttachVerbForTest(t, deps, "prompt-submit", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Len(t, pendingRows(t, root), 1, "prompt-submit leaves the queue for the next session-start")
}

func TestGlue_DrainFailure_AlertsAndStillRunsVerb(t *testing.T) {
	st := testutil.OperatorState(t)
	adapter := &recordingAdapter{}
	deps := attachTestDeps(st, 2, map[string]attach.Adapter{"claude-code": adapter})
	deps.drain = func(context.Context, attach.GlueDecision) error { return os.ErrPermission }
	_, stderr, err := runAttachVerbForTest(t, deps, "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Contains(t, stderr, "could not drain the attach queue")
	assert.Len(t, adapter.calls, 1)
	assert.Len(t, attachAlerts(t, st), 1)
}

// The glue path never imports and never triggers hooks, whatever sync.auto_sync says,
// including in a linked worktree of a listed board (MTIX-112.19.8; no glue auto-import exists).
func TestGlue_AttachedWorktree_RunsNoImportAndNoHookTrigger(t *testing.T) {
	for _, autoSync := range []string{"false", "true"} {
		t.Run("auto_sync_"+autoSync, func(t *testing.T) {
			st := testutil.OperatorState(t)
			root := listedBoardCheckout(t, st)
			require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "config.yaml"), []byte("sync:\n  auto_sync: "+autoSync+"\n"), 0o600))
			tasks := filepath.Join(root, ".mtix", "tasks.json")
			require.NoError(t, os.WriteFile(tasks, []byte(`{"nodes":[{"id":"X-1","title":"would be imported"}]}`), 0o600))
			hookMarker := filepath.Join(root, "hook-ran")
			require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "hooks.dispatch"), []byte("#!/bin/sh\ntouch "+hookMarker+"\n"), 0o700))
			wt := linkedWorktreeOf(t, root)
			t.Chdir(wt)
			before := nodeCount(t, root)

			_, stderr, err := runAttachVerbForTest(t, productionGlueDeps(st, &recordingAdapter{}), "session-start", "--harness", "claude-code", "--protocol", "2")
			require.NoError(t, err)
			assert.Empty(t, stderr)
			assert.Equal(t, before, nodeCount(t, root), "glue imported tasks.json")
			_, statErr := os.Stat(hookMarker)
			assert.True(t, os.IsNotExist(statErr), "glue triggered a hook")
			raw, readErr := os.ReadFile(tasks)
			require.NoError(t, readErr)
			assert.Contains(t, string(raw), "would be imported", "glue rewrote tasks.json (an export)")
		})
	}
}

// linkedWorktreeOf lays out a linked worktree of root the way git does.
func linkedWorktreeOf(t *testing.T, root string) string {
	t.Helper()
	wt := t.TempDir()
	admin := filepath.Join(root, ".git", "worktrees", "wt")
	require.NoError(t, os.MkdirAll(admin, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+admin+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte("../..\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(wt, ".git")+"\n"), 0o644))
	return wt
}

func nodeCount(t *testing.T, root string) int {
	t.Helper()
	store, err := sqlite.New(filepath.Join(root, ".mtix", "data"), slog.Default())
	require.NoError(t, err)
	defer func() { require.NoError(t, store.Close()) }()
	var n int
	require.NoError(t, store.ReadDB().QueryRowContext(t.Context(), "SELECT COUNT(*) FROM nodes").Scan(&n))
	return n
}

// operatorEntries lists the operator directory's files, so a test can prove a
// verb created or changed nothing there.
func operatorEntries(t *testing.T, st *oplocal.State) map[string]string {
	t.Helper()
	dir, err := st.Path()
	require.NoError(t, err)
	out := map[string]string{}
	if _, statErr := os.Stat(dir); os.IsNotExist(statErr) {
		return out // an operator directory that was never created holds nothing
	}
	require.NoError(t, filepath.WalkDir(dir, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !d.IsDir() {
			raw, readErr := os.ReadFile(p) //nolint:gosec // test reads files it just walked
			require.NoError(t, readErr)
			out[p] = string(raw)
		}
		return nil
	}))
	return out
}

// Every failure path of an attach verb, in a directory that is not an attached
// board, prints only the not-attached line and writes nothing.
func TestGlue_UnlistedDir_EveryFailurePathPrintsOneLineAndWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{"unknown verb", []string{"attach", "frobnicate", "--harness", "pi", "--protocol", "1"}},
		{"verb without arguments", []string{"attach"}},
		{"protocol flag without a value", []string{"attach", "session-start", "--harness", "pi", "--protocol"}},
		{"malformed root flag", []string{"attach", "session-start", "--harness", "pi", "--protocol", "1", "--json=x"}},
		{"project directory missing", []string{"-C", "/nonexistent/mtix-attach-test", "attach", "session-start", "--harness", "pi", "--protocol", "1"}},
		{"unknown harness", []string{"attach", "session-start", "--harness", "nope", "--protocol", "1"}},
		{"protocol out of range", []string{"attach", "session-start", "--harness", "pi", "--protocol", "99"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saveAndResetApp(t)
			st := testutil.OperatorState(t)
			other := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(other, ".git"), 0o755))
			t.Chdir(other)
			before := operatorEntries(t, st)
			root := newRootCmd()
			var errOut bytes.Buffer
			root.SetErr(&errOut)
			root.SetOut(&bytes.Buffer{})
			root.SetArgs(tt.args)
			require.NoError(t, root.ExecuteContext(context.Background()), "hook verbs always exit 0")
			assert.Equal(t, "board not attached on this host\n", errOut.String())
			assert.Equal(t, before, operatorEntries(t, st), "nothing is written in an unattached directory")
		})
	}
}

// A listed linked worktree drains its own store's queue: a valid row is applied,
// a forged one is dropped (S1: the queue of the directory the session runs in).
func TestGlue_ListedWorktree_DrainsItsOwnQueueAndDropsForgedRow(t *testing.T) {
	st := testutil.OperatorState(t)
	mainRoot := listedBoardCheckout(t, st)
	wt := linkedWorktreeOf(t, mainRoot)
	dataDir := filepath.Join(wt, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	store, err := sqlite.New(dataDir, slog.Default())
	require.NoError(t, err)
	id, err := store.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, st.ListBoardInstance(t.Context(), oplocal.ListedBoard{Key: id, Path: wt, StoreInstanceID: id}))
	validPayload, err := json.Marshal(map[string]string{"agent_id": "wt-agent", "harness": "claude-code", "lane_path": wt})
	require.NoError(t, err)
	enqueueRow(t, wt, sqlite.AttachPending{Kind: "lane", BoardKey: id, Path: wt, Payload: string(validPayload), QueuedAt: "now"})
	enqueueRow(t, wt, forgedRow(t, id, t.TempDir()))
	t.Chdir(wt)

	_, stderr, err := runAttachVerbForTest(t, productionGlueDeps(st, &recordingAdapter{}), "session-start", "--harness", "claude-code", "--protocol", "2")
	require.NoError(t, err)
	assert.Empty(t, stderr)
	assert.Empty(t, pendingRows(t, wt), "both rows leave the worktree queue: one applied, one dropped")
	alerts := attachAlerts(t, st)
	require.Len(t, alerts, 1, "only the forged row alerts")
	assert.Equal(t, "attach.pending-rejected", alerts[0].Class)
}

// A listed board reached through a symlinked working directory and a listed
// board with no git checkout both act (they were declined as unattached).
func TestGlue_ProductionPredicate_SymlinkedCwdAndNonGitBoard_Act(t *testing.T) {
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	link := filepath.Join(t.TempDir(), "via-link")
	require.NoError(t, os.Symlink(root, link))
	t.Chdir(link)
	assert.True(t, productionGluePredicate(t.Context()).Act, "symlinked cwd")

	plain := t.TempDir()
	dataDir := filepath.Join(plain, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	store, err := sqlite.New(dataDir, slog.Default())
	require.NoError(t, err)
	id, err := store.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NoError(t, store.Close())
	require.NoError(t, st.ListBoardInstance(t.Context(), oplocal.ListedBoard{Key: id, Path: plain, StoreInstanceID: id}))
	t.Chdir(plain)
	assert.True(t, productionGluePredicate(t.Context()).Act, "non-git board")
}

// The real attach command, built the way main builds it, drains a forged row on
// session-start: removing the drain from the production deps, or the drain call,
// fails this test.
func TestGlue_ProductionCommand_SessionStartDropsForgedRow(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	root := listedBoardCheckout(t, st)
	boards, err := st.DispatchBoards(t.Context())
	require.NoError(t, err)
	enqueueRow(t, root, forgedRow(t, boards.Listed[0].Key, t.TempDir()))
	t.Chdir(root)
	cmd := newRootCmd()
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"attach", "session-start", "--harness", "pi", "--protocol", "1"})
	require.NoError(t, cmd.ExecuteContext(t.Context()))
	assert.Empty(t, pendingRows(t, root), "the production command drained the queue")
}
