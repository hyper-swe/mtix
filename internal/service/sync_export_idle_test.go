// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

// Idle exports must not become writes when only the injected timestamp changes.
func TestAutoExport_IdleTicksPreservePublishedFile(t *testing.T) {
	f := newIdleExportFixture(t, slog.LevelDebug)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	marker := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, marker, marker))
	first := idleExportBytes(t, path)
	info, err := os.Stat(path)
	require.NoError(t, err)
	for tick := 1; tick <= 20; tick++ {
		f.advance()
		require.NoError(t, f.svc.AutoExport(ctx, f.dir))
		require.Equal(t, first, idleExportBytes(t, path), "tick %d changed board bytes", tick)
		after, err := os.Stat(path)
		require.NoError(t, err)
		require.Equal(t, info.ModTime(), after.ModTime(), "tick %d changed mtime", tick)
		require.True(t, os.SameFile(info, after), "tick %d replaced the published file", tick)
		assertIdleExportMetadata(t, f, first)
	}
	// A new service must compare disk content, rather than an in-memory cache.
	restarted := service.NewSyncService(f.store, f.logger, f.clock)
	require.NoError(t, restarted.AutoExport(ctx, f.dir))
	require.Equal(t, first, idleExportBytes(t, path))
	require.Equal(t, 1, strings.Count(f.logs.String(), "msg=sync_export_completed"))
}

func TestAutoExport_RealChangePublishesOnce(t *testing.T) {
	f := newIdleExportFixture(t, slog.LevelDebug)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	first := idleExportBytes(t, path)
	f.advance()
	addIdleExportNode(t, f.store, 2)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	changed := idleExportBytes(t, path)
	require.NotEqual(t, first, changed)
	info, err := os.Stat(path)
	require.NoError(t, err)
	f.advance()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, changed, idleExportBytes(t, path))
	after, err := os.Stat(path)
	require.NoError(t, err)
	require.True(t, os.SameFile(info, after))
	require.Equal(t, 2, strings.Count(f.logs.String(), "msg=sync_export_completed"), "one completed publication per content change")
	assertIdleExportMetadata(t, f, changed)
}

func TestAutoExport_DefaultLogsContainNoExportLines(t *testing.T) {
	f := newIdleExportFixture(t, slog.LevelInfo)
	ctx := context.Background()
	for tick := 0; tick < 10; tick++ {
		f.advance()
		require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	}
	addIdleExportNode(t, f.store, 2)
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.NotContains(t, f.logs.String(), "sync_export", "default logging must stay quiet on idle and successful changed exports")
}
