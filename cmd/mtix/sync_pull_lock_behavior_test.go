// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Pull commands preserve local state and ownership when a sync writer is busy.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func pullLockSnapshot(t *testing.T) []byte {
	t.Helper()
	var meta string
	require.NoError(t, app.store.QueryRow(t.Context(), `SELECT COALESCE(group_concat(key || '=' || value, '|'),'') FROM (SELECT key,value FROM meta ORDER BY key)`).Scan(&meta))
	board, err := app.store.Export(t.Context(), "TEST", "lock-test")
	require.NoError(t, err)
	require.Len(t, board.Nodes, 1)
	board.ExportedAt = "" // Export wall time is not board state.
	journal, err := app.store.ReadRelayJournalSince(t.Context(), 0, 100)
	require.NoError(t, err)
	require.Len(t, journal, 1)
	tail, err := app.store.JournalTail(t.Context())
	require.NoError(t, err)
	files := map[string]string{}
	for _, name := range []string{"tasks.json", "data/exchange-record.json"} {
		raw, readErr := os.ReadFile(filepath.Join(app.mtixDir, name))
		if os.IsNotExist(readErr) {
			files[name] = "absent"
			continue
		}
		require.NoError(t, readErr)
		files[name] = string(raw)
	}
	raw, err := json.Marshal(struct {
		Meta    string
		Tail    int64
		Files   map[string]string
		Board   *sqlite.ExportData
		Journal []sqlite.RelayJournalEvent
	}{meta, tail, files, board, journal})
	require.NoError(t, err)
	return raw
}

func executePullLockCommand(t *testing.T, ctx context.Context) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	cmd := newSyncPullCmd()
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(nil)
	err := cmd.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

func TestSyncPullLock_HookContendedOwner_WarnsAndSkips(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "1")
	t.Setenv("MTIX_SYNC_DSN", "")
	initTestApp(t)
	seedPullLockBoard(t)
	owner, err := app.syncSvc.BeginExchangePull(t.Context(), app.mtixDir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	before := pullLockSnapshot(t)
	stdout, stderr, err := executePullLockCommand(t, t.Context())
	require.NoError(t, err)
	require.Zero(t, exitCodeForError(err))
	require.Empty(t, stdout)
	for _, text := range []string{"WARN:", "sync lock", "pull not performed", "retry after the current owner finishes"} {
		require.Contains(t, stderr, text)
	}
	require.Equal(t, before, pullLockSnapshot(t))
	_, err = app.syncSvc.BeginExchangePull(t.Context(), app.mtixDir)
	require.ErrorIs(t, err, service.ErrNoExchangeRecord)
	owner.Close()
	next, err := app.syncSvc.BeginExchangePull(t.Context(), app.mtixDir)
	require.NoError(t, err)
	next.Close()
}

func TestSyncPullLock_NonhookContendedOwner_ReturnsPullError(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "")
	initTestApp(t)
	seedPullLockBoard(t)
	owner, err := app.syncSvc.BeginExchangePull(t.Context(), app.mtixDir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	before := pullLockSnapshot(t)
	stdout, stderr, err := executePullLockCommand(t, t.Context())
	require.Error(t, err)
	require.NotZero(t, exitCodeForError(err))
	require.Contains(t, err.Error(), "mtix sync pull sync lock")
	require.ErrorIs(t, err, service.ErrExchangeLockBusy)
	require.NotContains(t, err.Error(), app.mtixDir)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	require.Equal(t, before, pullLockSnapshot(t))
}

func TestSyncPullLock_HookFilesystemFailure_RemainsFatal(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "1")
	initTestApp(t)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "data"), []byte("owned obstruction"), 0600))
	app.mtixDir = dir
	stdout, stderr, err := executePullLockCommand(t, t.Context())
	require.Error(t, err)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	raw, readErr := os.ReadFile(filepath.Join(dir, "data"))
	require.NoError(t, readErr)
	require.Equal(t, "owned obstruction", string(raw))
}

func TestSyncPullLock_HookCancellationAndMissingDSN_RemainFatal(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "1")
	t.Setenv("MTIX_SYNC_DSN", "")
	initTestApp(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	stdout, stderr, err := executePullLockCommand(t, ctx)
	require.Error(t, err)
	require.Empty(t, stdout)
	require.Empty(t, stderr)
	var out, diagnostic bytes.Buffer
	err = runSyncPull(t.Context(), &out, &diagnostic, nil, transport.Options{}, 1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no DSN configured")
	require.Empty(t, diagnostic.String())
}

func TestSyncPullLock_HookPriorReceiptAndJournal_Preserved(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "1")
	initTestApp(t)
	seedPullLockBoard(t)
	preparePullLockPriorReceipt(t)
	owner, err := app.syncSvc.BeginExchangePull(t.Context(), app.mtixDir)
	require.NoError(t, err)
	t.Cleanup(owner.Close)
	before := pullLockSnapshot(t)
	stdout, stderr, err := executePullLockCommand(t, t.Context())
	require.NoError(t, err)
	require.Empty(t, stdout)
	require.Contains(t, stderr, "pull not performed")
	require.Equal(t, before, pullLockSnapshot(t))
}

func seedPullLockBoard(t *testing.T) {
	t.Helper()
	require.NoError(t, app.store.CreateNode(t.Context(), mkPGNode("TEST-1", "", 0, 1, "local work")))
	require.NoError(t, app.syncSvc.AutoExport(t.Context(), app.mtixDir))
}

func preparePullLockPriorReceipt(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("receipt publication requires a supported native platform")
	}
	pending, err := readPendingBatch(t.Context(), app.store, 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	// Model an accepted acknowledgement without claiming a network exchange.
	require.NoError(t, markPushed(t.Context(), app.store, []string{pending[0].EventID}))
	prior, err := app.syncSvc.BeginExchangePull(t.Context(), app.mtixDir)
	require.NoError(t, err)
	t.Cleanup(prior.Close)
	var stderr bytes.Buffer
	in := testIngest(&stderr)
	in.receipt = newPullExchangeTracker(app.syncSvc.MaxImportSize)
	hub := &fakeLateHub{hubNows: []time.Time{sweepHubT1}}
	outcome, err := finishExchangeTestPull(t, prior, in, hub)
	require.NoError(t, err)
	require.True(t, outcome.sweep.Completed)
	prior.Close()
	require.FileExists(t, filepath.Join(app.mtixDir, "data", "exchange-record.json"))
	record, err := app.syncSvc.LastExchanged(t.Context(), app.mtixDir)
	require.NoError(t, err)
	require.Equal(t, "hub-pull", record.Source)
	require.Equal(t, 1, record.NodeCount)
}
