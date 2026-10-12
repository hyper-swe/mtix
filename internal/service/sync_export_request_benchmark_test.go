// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// BenchmarkPendingExportRequest_Coalesced measures real native durable mark IO.
// Identical code runs on the unchanged baseline and coalesced implementation.
// Export/reset IO is outside the measured interval; no filesystem calls are mocked.
func BenchmarkPendingExportRequest_Coalesced(b *testing.B) {
	for _, clean := range []bool{true, false} {
		name := map[bool]string{true: "clean-to-pending", false: "already-pending"}[clean]
		b.Run(name, func(b *testing.B) { benchmarkPendingExportRequest(b, clean) })
	}
}

func benchmarkPendingExportRequest(b *testing.B, clean bool) {
	b.StopTimer()
	dir := b.TempDir()
	require.NoError(b, os.MkdirAll(filepath.Join(dir, "data"), 0755))
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	store, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), logger)
	require.NoError(b, err)
	b.Cleanup(func() { require.NoError(b, store.Close()) })
	clock := func() time.Time { return time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC) }
	store.SetClock(clock)
	svc := NewSyncService(store, logger, clock)
	ctx := context.Background()
	require.NoError(b, svc.ForceExport(ctx, dir))
	if !clean {
		require.NoError(b, svc.RequestExport(dir))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if clean {
			require.NoError(b, svc.ForceExport(ctx, dir))
		}
		b.StartTimer()
		err = svc.RequestExport(dir)
		b.StopTimer()
		require.NoError(b, err)
	}
	requests, err := pendingExportRequests(dir)
	require.NoError(b, err)
	b.ReportMetric(float64(len(requests)), "request-files")
	require.NoError(b, svc.DrainPendingExport(ctx, dir))
}
