// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// Exercise the actual native daemon drain on controlled ticks, with no sleeps.
func TestDaemon_IdleExportTicksLeaveBoardUntouched(t *testing.T) {
	dir, advance, logs := daemonIdleExportFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	ticks := make(chan time.Time)
	observed := make(chan bool, 1)
	done := make(chan error, 1)
	control := daemonPassControl{ticks: ticks, observed: func(acquired bool) {
		select {
		case observed <- acquired:
		case <-ctx.Done():
		}
	}}
	var stdout, stderr bytes.Buffer
	go func() { done <- runDaemon(ctx, &stdout, &stderr, nil, transport.Options{}, 3600, control) }()
	joined := false
	t.Cleanup(func() {
		cancel()
		if !joined {
			awaitDispatchDone(t, done)
		}
	})
	require.True(t, awaitDispatchObservation(t, observed))
	path := filepath.Join(dir, "tasks.json")
	first, err := os.ReadFile(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	for tick := 1; tick <= 10; tick++ {
		advance()
		require.NoError(t, app.syncSvc.RequestExport(dir))
		sendDispatchTick(t, ticks, done)
		require.True(t, awaitDispatchObservation(t, observed))
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		require.Equal(t, first, raw, "idle daemon tick %d changed bytes", tick)
		after, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, info.ModTime(), after.ModTime(), "idle daemon tick %d changed mtime", tick)
		require.True(t, os.SameFile(info, after))
	}
	cancel()
	awaitDispatchDone(t, done)
	joined = true
	require.NotContains(t, logs.String(), "sync_export", "idle daemon must not accumulate export logs at default level")
	require.Empty(t, stderr.String())
}

func daemonIdleExportFixture(t *testing.T) (string, func(), *bytes.Buffer) {
	t.Helper()
	saveAndResetApp(t)
	t.Setenv(transport.EnvDSN, "")
	t.Setenv("MTIX_PG_TEST_DSN", "")
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0755))
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	store, err := sqlite.New(filepath.Join(dir, "data"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	base := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	var minutes atomic.Int64
	clock := func() time.Time { return base.Add(time.Duration(minutes.Load()) * time.Minute) }
	store.SetClock(clock)
	node := &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "Idle daemon node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1,
		NodeType: model.NodeTypeIssue, CreatedAt: base, UpdatedAt: base}
	node.ContentHash = node.ComputeHash()
	require.NoError(t, store.CreateNode(t.Context(), node))
	svc := service.NewSyncService(store, logger, clock)
	require.NoError(t, svc.AutoExport(t.Context(), dir))
	marker := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(filepath.Join(dir, "tasks.json"), marker, marker))
	logs.Reset()
	app = appContext{mtixDir: dir, store: store, syncSvc: svc, logger: logger,
		hooksDisp: service.NewHooksDispatcher(store, dir, logger)}
	return dir, func() { minutes.Add(1) }, logs
}
