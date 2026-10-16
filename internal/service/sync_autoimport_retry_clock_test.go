// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAutoImport_SyncLockRetryClock_OverridePreservesDomainClock(t *testing.T) {
	original, dir := retryImportFixture(t)
	domainTime := original.clock()
	var retryTicks atomic.Int64
	retryClock := func() time.Time {
		return domainTime.Add(time.Hour + time.Duration(retryTicks.Add(1))*time.Second)
	}
	svc := NewSyncService(original.store, original.logger, original.clock, retryClock)
	require.NoError(t, svc.RequestExport(dir))
	status, err := svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, 1, status.Count)
	require.Equal(t, domainTime.UTC().Format(time.RFC3339Nano), status.OldestAt,
		"a retry override must not timestamp domain work")
	require.Zero(t, status.OldestAgeSeconds, "pending age keeps its original domain clock")
	require.Zero(t, retryTicks.Load(), "metadata work must not consume the retry clock")
	lock, err := svc.acquireLock(dir, lockExclusive)
	require.NoError(t, err)
	defer svc.releaseLock(lock)
	require.ErrorIs(t, svc.AutoImport(context.Background(), dir), ErrAutoImportLockBusy)
	require.Equal(t, int64(2), retryTicks.Load(), "start then the contended attempt's elapsed cap")
	status, err = svc.PendingExports(context.Background(), dir)
	require.NoError(t, err)
	require.Equal(t, domainTime.UTC().Format(time.RFC3339Nano), status.OldestAt)
	require.Zero(t, status.OldestAgeSeconds)
}
