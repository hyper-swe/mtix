// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service_test

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestAnnotation_BusySyncLock_ExportAndImportSkipsKeepAnnotation proves that
// a comment written while another process holds data/sync.lock is never lost:
// the skipped auto-export leaves the stored annotation in the database and a
// pending export request, the skipped and the later auto-import keep it, and
// the first auto-export after the lock is free publishes it to tasks.json
// (MTIX-148, FR-15.3, MTIX-95.31.2).
func TestAnnotation_BusySyncLock_ExportAndImportSkipsKeepAnnotation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o755))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	clock := func() time.Time { return time.Date(2026, 10, 12, 0, 0, 0, 0, time.UTC) }
	st, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	ctx := context.Background()
	nodeSvc := service.NewNodeService(st, nil, &service.StaticConfig{}, logger, clock)
	node, err := nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "locked board"})
	require.NoError(t, err)
	syncSvc := service.NewSyncService(st, logger, clock)
	require.NoError(t, syncSvc.ForceExport(ctx, dir))
	promptSvc := service.NewPromptService(st, nil, logger, clock)

	// Another process holds the sync lock (a push, an import or an export).
	holder, err := os.OpenFile(filepath.Join(dir, "data", "sync.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX))

	require.NoError(t, promptSvc.AddAnnotation(ctx, node.ID, "written while locked", "agent", ""))
	require.NoError(t, syncSvc.AutoExport(ctx, dir), "a busy lock skips the export without failing the command")
	require.NoError(t, syncSvc.AutoImport(ctx, dir), "a busy lock skips the import without failing the command")
	pending, err := syncSvc.PendingExports(ctx, dir)
	require.NoError(t, err)
	require.Equal(t, 1, pending.Count, "the skipped export must stay recorded as pending")
	assertStoredAnnotations(t, st, node.ID, "written while locked")
	require.NotContains(t, readTasksJSON(t, dir), "written while locked", "the file has not been exported yet")

	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_UN))
	require.NoError(t, holder.Close())

	require.NoError(t, syncSvc.AutoImport(ctx, dir), "the next import must not replace the board with the stale file")
	assertStoredAnnotations(t, st, node.ID, "written while locked")
	require.NoError(t, syncSvc.AutoExport(ctx, dir))
	require.Contains(t, readTasksJSON(t, dir), "written while locked", "the first export after the lock frees publishes it")
	pending, err = syncSvc.PendingExports(ctx, dir)
	require.NoError(t, err)
	require.Zero(t, pending.Count)
	assertStoredAnnotations(t, st, node.ID, "written while locked")
}

func assertStoredAnnotations(t *testing.T, st *sqlite.Store, id string, want ...string) {
	t.Helper()
	got, err := st.GetNode(context.Background(), id)
	require.NoError(t, err)
	texts := make([]string, 0, len(got.Annotations))
	for _, a := range got.Annotations {
		texts = append(texts, a.Text)
	}
	require.ElementsMatch(t, want, texts)
}

func readTasksJSON(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "tasks.json"))
	require.NoError(t, err)
	return string(data)
}
