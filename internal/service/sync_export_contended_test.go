// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// contendedWarningHandler records each contended-export warning together
// with the pending export count observed at the moment it is logged.
type contendedWarningHandler struct {
	svc     *SyncService
	dir     string
	records *[]contendedWarning
}

type contendedWarning struct {
	level   slog.Level
	message string
	err     string
	pending int
}

func (contendedWarningHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h contendedWarningHandler) WithAttrs([]slog.Attr) slog.Handler     { return h }
func (h contendedWarningHandler) WithGroup(string) slog.Handler          { return h }
func (h contendedWarningHandler) Handle(ctx context.Context, record slog.Record) error {
	if record.Message != contendedExportWarning {
		return nil
	}
	got := contendedWarning{level: record.Level, message: record.Message, pending: -1}
	record.Attrs(func(attr slog.Attr) bool {
		if attr.Key == "error" {
			got.err = attr.Value.String()
		}
		return true
	})
	if status, err := h.svc.PendingExports(ctx, h.dir); err == nil {
		got.pending = status.Count
	}
	*h.records = append(*h.records, got)
	return nil
}

// TestExportBoardPass_ContendedWarningSaysDeferred pins the contended-lock
// warning text and proves a durable pending export request exists whenever
// it is logged (MTIX-130.19). The text points only at the read-only
// mtix sync status, never at mtix sync --fix, which discards a pulled board.
func TestExportBoardPass_ContendedWarningSaysDeferred(t *testing.T) {
	require.Equal(t, "sync lock busy: auto-export deferred; pending export request recorded "+
		"and will be retried; run 'mtix sync status' to check it", contendedExportWarning)
	require.NotContains(t, contendedExportWarning, "skipping")
	require.NotContains(t, contendedExportWarning, "--fix")

	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Zero(t, status.Count)

	var records []contendedWarning
	svc.logger = slog.New(contendedWarningHandler{svc: svc, dir: dir, records: &records})
	holdSyncLock(t, dir)

	require.NoError(t, svc.AutoExport(context.Background(), dir))
	require.Len(t, records, 1)
	require.Equal(t, slog.LevelWarn, records[0].level)
	require.Equal(t, contendedExportWarning, records[0].message)
	require.Contains(t, records[0].err, "acquire sync lock", "the structured error field is kept")
	require.GreaterOrEqual(t, records[0].pending, 1, "a durable pending export request exists when the warning is logged")
}

// TestAutoExport_RecordFailsUnderBusyLock_ReturnsErrorWithoutWarning injects a
// failing request record (the legacy request directory is a regular file, so
// RequestExport fails) under a busy lock: the deferred-and-recorded warning
// must not be logged as if a request existed, and the record error is returned.
func TestAutoExport_RecordFailsUnderBusyLock_ReturnsErrorWithoutWarning(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.AutoExport(context.Background(), dir))
	var records []contendedWarning
	svc.logger = slog.New(contendedWarningHandler{svc: svc, dir: dir, records: &records})

	require.NoError(t, os.WriteFile(pendingExportDir(dir), []byte("not a directory"), 0o600))
	holdSyncLock(t, dir)

	err := svc.AutoExport(context.Background(), dir)
	require.Error(t, err)
	require.Contains(t, err.Error(), "read pending export requests", "the record error is returned")
	require.Empty(t, records, "no deferred-and-recorded warning when recording the request failed")
}

// holdSyncLock holds the exclusive sync lock of dir until the test ends.
func holdSyncLock(t *testing.T, dir string) {
	t.Helper()
	holder, err := os.OpenFile(filepath.Join(dir, "data", "sync.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, holder.Close()) })
	require.NoError(t, syscall.Flock(int(holder.Fd()), syscall.LOCK_EX))
}
