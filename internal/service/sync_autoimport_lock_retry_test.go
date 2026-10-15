// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Auto-import contention must be distinguishable from a completed freshness check.
package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func retryImportFixture(t *testing.T) (*SyncService, string) {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0755))
	store, err := sqlite.New(filepath.Join(dir, "data"), logger)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	return NewSyncService(store, logger, func() time.Time {
		return time.Date(2026, 10, 14, 0, 0, 0, 0, time.UTC)
	}), dir
}

func TestAutoImport_SyncLockHeld_ReportsUnverifiedFreshness(t *testing.T) {
	svc, dir := retryImportFixture(t)
	lock, err := svc.acquireLock(dir, lockExclusive)
	require.NoError(t, err)
	t.Cleanup(func() { svc.releaseLock(lock) })
	err = svc.AutoImport(context.Background(), dir)
	require.ErrorIs(t, err, ErrAutoImportLockBusy, "busy ownership cannot silently report a successful import check")
}

func writeRetryIncoming(t *testing.T, target string) {
	t.Helper()
	source, _ := retryImportFixture(t)
	now := source.clock()
	node := &model.Node{ID: "TEST-1", Project: "TEST", Seq: 1, Title: "Incoming task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, NodeType: model.NodeTypeEpic,
		Weight: 1, CreatedAt: now, UpdatedAt: now}
	node.ContentHash = node.ComputeHash()
	require.NoError(t, source.store.CreateNode(context.Background(), node))
	board, err := source.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	raw, err := json.Marshal(board)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(target, "tasks.json"), raw, 0644))
}

func TestAutoImport_SyncLockBusy_RetryBudgetIsBounded(t *testing.T) {
	for _, clockStep := range []time.Duration{0, autoImportLockRetryInterval, -time.Second, 500 * time.Millisecond, time.Second} {
		t.Run(clockStep.String(), func(t *testing.T) {
			svc, dir := retryImportFixture(t)
			writeRetryIncoming(t, dir)
			lock, err := svc.acquireLock(dir, lockExclusive)
			require.NoError(t, err)
			defer svc.releaseLock(lock)
			now, elapsed, waits := svc.clock(), time.Duration(0), 0
			svc.clock = func() time.Time { return now }
			svc.retrySleep = func(_ context.Context, delay time.Duration) error {
				elapsed += delay
				now = now.Add(clockStep)
				waits++
				return nil
			}
			require.NoError(t, svc.RequestExport(dir)) // recovery must not repeat the budget
			require.ErrorIs(t, svc.AutoImport(context.Background(), dir), ErrAutoImportLockBusy)
			require.Positive(t, waits)
			require.LessOrEqual(t, elapsed, 500*time.Millisecond)
			if clockStep <= autoImportLockRetryInterval {
				require.Equal(t, 500*time.Millisecond, elapsed)
			}
			if clockStep >= autoImportLockRetryBudget {
				require.Equal(t, 1, waits, "wall clock advancement must shorten the scheduled wait budget")
				require.Equal(t, autoImportLockRetryInterval, elapsed)
			}
			_, err = svc.store.GetNode(context.Background(), "TEST-1")
			require.ErrorIs(t, err, model.ErrNotFound)
			_, err = os.Stat(filepath.Join(dir, "data", "sync.sha256"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestAutoImport_SyncLockReleasedDuringRetry_ImportsChangedBoard(t *testing.T) {
	svc, dir := retryImportFixture(t)
	writeRetryIncoming(t, dir)
	lock, err := svc.acquireLock(dir, lockExclusive)
	require.NoError(t, err)
	t.Cleanup(func() {
		if lock != nil {
			svc.releaseLock(lock)
		}
	})
	elapsed, waits := time.Duration(0), 0
	svc.retrySleep = func(_ context.Context, delay time.Duration) error {
		elapsed += delay
		waits++
		if waits == 2 {
			svc.releaseLock(lock)
			lock = nil
		}
		return nil
	}
	svc.SetNoticeWriter(io.Discard)
	require.NoError(t, svc.AutoImport(context.Background(), dir))
	require.Equal(t, 2, waits)
	require.Equal(t, 50*time.Millisecond, elapsed)
	node, err := svc.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "Incoming task", node.Title)
	hash, err := os.ReadFile(filepath.Join(dir, "data", "sync.sha256"))
	require.NoError(t, err)
	require.NotEmpty(t, hash)
	// A successful check on the same service carries no busy outcome forward.
	require.NoError(t, svc.AutoImport(context.Background(), dir))
}

func TestAutoImport_SyncLockRetry_CancellationAndFilesystemFailures(t *testing.T) {
	for _, mode := range []string{"already cancelled", "cancel during wait", "invalid lock file"} {
		t.Run(mode, func(t *testing.T) {
			svc, dir := retryImportFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			waits := 0
			svc.retrySleep = func(ctx context.Context, _ time.Duration) error {
				waits++
				cancel()
				return ctx.Err()
			}
			if mode == "invalid lock file" {
				require.NoError(t, os.Mkdir(filepath.Join(dir, "data", "sync.lock"), 0755))
			} else {
				lock, err := svc.acquireLock(dir, lockExclusive)
				require.NoError(t, err)
				defer svc.releaseLock(lock)
				if mode == "already cancelled" {
					cancel()
				}
			}
			err := svc.AutoImport(ctx, dir)
			require.Error(t, err)
			require.NotErrorIs(t, err, ErrAutoImportLockBusy)
			if mode != "invalid lock file" {
				require.ErrorIs(t, err, context.Canceled)
			}
			if mode == "cancel during wait" {
				require.Equal(t, 1, waits)
			} else {
				require.Zero(t, waits)
			}
		})
	}
}

func TestWaitAutoImportRetry_CancelledContext_ReturnsWithoutWaiting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, waitAutoImportRetry(ctx, time.Hour), context.Canceled)
	require.NoError(t, waitAutoImportRetry(context.Background(), 0))
}
