// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package service

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPendingExports_CancelAfterPublicationRetiresOnlyCutoff(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.RequestExport(dir))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	late := pendingProcessClock().Add(time.Hour)
	svc.logger = slog.New(pendingExportHandler{callback: func() {
		pendingProcessNode(t, store, 2)
		svc.clock = func() time.Time { return late }
		require.NoError(t, svc.RequestExport(dir))
		cancel()
	}})
	require.NoError(t, svc.exportBoardProtected(ctx, dir, true))
	require.Equal(t, 1, pendingProcessMirrorCount(t, dir))
	state, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.Equal(t, int64(1), state.Covered)
	require.Equal(t, int64(2), state.Requested)
	require.Equal(t, late.Format(time.RFC3339), state.Oldest)
}

func TestPendingExports_CancelledOrFailedPublicationNeverRetires(t *testing.T) {
	for _, stage := range []string{"before-snapshot", "during-publication", "retirement-error"} {
		t.Run(stage, func(t *testing.T) { pendingCancelledPublication(t, stage) })
	}
}

func pendingCancelledPublication(t *testing.T, stage string) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.RequestExport(dir))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if stage == "before-snapshot" {
		cancel()
	} else {
		store.SetClock(func() time.Time {
			if stage == "during-publication" {
				cancel()
			} else {
				require.NoError(t, store.WithTx(context.Background(), func(tx *sql.Tx) error {
					_, err := tx.ExecContext(context.Background(), "CREATE TRIGGER owned_retirement_failure BEFORE INSERT ON meta WHEN NEW.key='meta.export.pending' BEGIN SELECT RAISE(ABORT,'owned retirement failure'); END")
					return err
				}))
			}
			return pendingProcessClock()
		})
	}
	err := svc.exportBoardProtected(ctx, dir, true)
	if stage == "retirement-error" {
		require.ErrorContains(t, err, "owned retirement failure")
	} else {
		require.ErrorIs(t, err, context.Canceled)
	}
	state, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.Zero(t, state.Covered)
	require.Equal(t, int64(1), state.Requested)
}

func TestPendingExports_RetirementDeadlineRetainsIntent(t *testing.T) {
	dir := t.TempDir()
	store := pendingProcessStore(t, dir)
	pendingProcessNode(t, store, 1)
	svc := NewSyncService(store, slog.Default(), pendingProcessClock)
	require.NoError(t, svc.RequestExport(dir))
	var writer *coalescingWriter
	svc.logger = slog.New(pendingExportHandler{callback: func() {
		writer = startExportRequestChild(t, dir, "hold-metadata-lock")
		writer.awaitSignal(t, "writer-held")
	}})
	err := svc.exportBoardProtected(context.Background(), dir, true)
	require.Error(t, err, "native SQLite writer contention must not run cleanup without its finite bound")
	require.True(t, errors.Is(err, context.DeadlineExceeded) || containsSQLiteBusy(err), "cleanup reports its actual deadline/SQLite busy cause: %v", err)
	_, releaseErr := io.WriteString(writer.stdin, "release\n")
	require.NoError(t, releaseErr)
	writer.awaitExit(t)
	state, err := store.ReadExportRequest(context.Background())
	require.NoError(t, err)
	require.Zero(t, state.Covered)
	require.Equal(t, int64(1), state.Requested)
	svc.logger = slog.Default()
	require.NoError(t, svc.DrainPendingExport(context.Background(), dir))
}

func containsSQLiteBusy(err error) bool {
	// The native driver's busy_timeout and the cleanup deadline share the same
	// five-second budget; either may report first, without an elapsed-time proof.
	return err != nil && (strings.Contains(err.Error(), "SQLITE_BUSY") || strings.Contains(err.Error(), "database is locked"))
}
