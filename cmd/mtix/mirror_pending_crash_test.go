// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"testing/synctest"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestMirrorPending_ProcessCrashBeforeDebounceRecovers(t *testing.T) {
	dir := t.TempDir()
	writer := startDaemonPendingChild(t, "predebounce", dir)
	writer.awaitReady(t)
	require.NoError(t, writer.cmd.Process.Kill())
	select {
	case <-writer.done:
		require.Error(t, writer.err, "owned writer was killed without flushing")
	case <-time.After(60 * time.Second):
		t.Fatal("killed writer did not join")
	}
	restart := startDaemonPendingChild(t, "restart", dir)
	restart.awaitExit(t)
	data, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	require.NoError(t, err)
	var board sqlite.ExportData
	require.NoError(t, json.Unmarshal(data, &board))
	require.Equal(t, 2, board.NodeCount, "restart must recover committed mutation before debounce without a new write")
}

func mirrorCrashHelper(t *testing.T, mode, dir string) {
	if mode == "restart" {
		store := newDaemonTestStore(t, dir)
		data, err := store.Export(context.Background(), "", "")
		require.NoError(t, err)
		require.Equal(t, 2, data.NodeCount, "killed child committed DB2")
		svc := service.NewSyncService(store, slog.Default(), time.Now)
		require.NoError(t, svc.AutoImport(context.Background(), dir))
		return
	}
	synctest.Test(t, func(t *testing.T) {
		// Construct SQL/service/debouncer goroutines inside this bubble. External
		// stdin I/O is not durably blocking, so fake time cannot reach debounce.
		store := newDaemonTestStore(t, dir)
		logger := slog.Default()
		svc := service.NewSyncService(store, logger, time.Now)
		mirrorCrashNode(t, store, 1)
		require.NoError(t, svc.AutoExport(context.Background(), dir))
		app = appContext{mtixDir: dir, store: store, logger: logger, syncSvc: svc}
		closeExporter := wireMirrorExporter(logger)
		defer closeExporter()
		mirrorCrashNode(t, store, 2)
		_, err := fmt.Fprintln(os.Stdout, "committed")
		require.NoError(t, err)
		_, err = bufio.NewReader(os.Stdin).ReadString('\n')
		// Cleanup can close stdin; intentional SIGKILL never executes this flush.
		if err != nil {
			return
		}
	})
}
func mirrorCrashNode(t *testing.T, store *sqlite.Store, seq int) {
	t.Helper()
	now := time.Now().UTC()
	require.NoError(t, store.CreateNode(context.Background(), &model.Node{
		ID: fmt.Sprintf("CRASH-%d", seq), Project: "CRASH", Seq: seq,
		Title: "Pre-debounce crash recovery", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1, NodeType: model.NodeTypeEpic,
		CreatedAt: now, UpdatedAt: now,
	}))
}

func TestMirrorPending_MarkFailureStillSchedulesWithoutInlineExport(t *testing.T) {
	synctest.Test(t, testMirrorMarkFailure)
}

func testMirrorMarkFailure(t *testing.T) {
	dir := t.TempDir()
	store := newDaemonTestStore(t, dir)
	logger := slog.Default()
	svc := service.NewSyncService(store, logger, time.Now)
	mirrorCrashNode(t, store, 1)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	pending := filepath.Join(dir, "data", "export-pending")
	require.NoError(t, os.Remove(pending))
	require.NoError(t, os.WriteFile(pending, []byte("owned unavailable metadata"), 0644))
	oldApp := app
	t.Cleanup(func() { app = oldApp })
	app = appContext{mtixDir: dir, store: store, logger: logger, syncSvc: svc}
	closeExporter := wireMirrorExporter(logger)
	t.Cleanup(closeExporter)
	mirrorCrashNode(t, store, 2)
	// The active bubble goroutine prevents fake time advancing to debounce;
	// native reads do not durably block it. No wall-clock speed assertion.
	require.Equal(t, 1, mirrorCrashCount(t, dir), "on-commit marking must not export inline")
	closeExporter() // Synchronously flushes work even though request marking failed.
	require.Equal(t, 2, mirrorCrashCount(t, dir), "failed mark must still schedule the native debouncer")
}
func mirrorCrashCount(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	require.NoError(t, err)
	var board sqlite.ExportData
	require.NoError(t, json.Unmarshal(data, &board))
	return board.NodeCount
}
