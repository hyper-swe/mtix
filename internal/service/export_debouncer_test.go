// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// countingExport returns an export fn that counts invocations.
func countingExport(count *atomic.Int64) func(context.Context) error {
	return func(context.Context) error {
		count.Add(1)
		return nil
	}
}

// TestExportDebouncer_CoalescesBurst: many triggers in quick succession
// produce a single export after the quiet period.
func TestExportDebouncer_CoalescesBurst(t *testing.T) {
	var count atomic.Int64
	d := service.NewExportDebouncer(countingExport(&count), slog.Default(),
		50*time.Millisecond, time.Second)

	for i := 0; i < 20; i++ {
		d.Trigger()
	}

	require.Eventually(t, func() bool { return count.Load() == 1 },
		2*time.Second, 10*time.Millisecond)

	// Quiet afterwards: no further exports.
	time.Sleep(150 * time.Millisecond)
	assert.Equal(t, int64(1), count.Load())
}

// TestExportDebouncer_MaxDelayBoundsStarvation: a sustained write burst
// (re-triggering faster than the quiet period) cannot postpone the export
// beyond maxDelay.
func TestExportDebouncer_MaxDelayBoundsStarvation(t *testing.T) {
	var count atomic.Int64
	d := service.NewExportDebouncer(countingExport(&count), slog.Default(),
		100*time.Millisecond, 300*time.Millisecond)

	stop := time.After(time.Second)
	tick := time.NewTicker(30 * time.Millisecond) // always inside quiet period
	defer tick.Stop()

burst:
	for {
		select {
		case <-stop:
			break burst
		case <-tick.C:
			d.Trigger()
		}
	}

	assert.GreaterOrEqual(t, count.Load(), int64(2),
		"maxDelay must force exports during a sustained burst")
}

// TestExportDebouncer_FlushExportsPendingImmediately.
func TestExportDebouncer_FlushExportsPendingImmediately(t *testing.T) {
	var count atomic.Int64
	d := service.NewExportDebouncer(countingExport(&count), slog.Default(),
		time.Hour, time.Hour) // would never fire on its own

	d.Trigger()
	d.Flush()
	assert.Equal(t, int64(1), count.Load())

	// Nothing pending: Flush is a no-op.
	d.Flush()
	assert.Equal(t, int64(1), count.Load())
}

// TestExportDebouncer_CloseFlushesAndStops: shutdown must write the final
// mirror, and later triggers are ignored.
func TestExportDebouncer_CloseFlushesAndStops(t *testing.T) {
	var count atomic.Int64
	d := service.NewExportDebouncer(countingExport(&count), slog.Default(),
		time.Hour, time.Hour)

	d.Trigger()
	d.Close()
	assert.Equal(t, int64(1), count.Load())

	d.Trigger()
	d.Flush()
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int64(1), count.Load(), "closed debouncer must ignore triggers")
}

// TestExportDebouncer_ExportFailureDoesNotPanic: mirror trouble is logged,
// never fatal to the interface.
func TestExportDebouncer_ExportFailureDoesNotPanic(t *testing.T) {
	d := service.NewExportDebouncer(
		func(context.Context) error { return context.DeadlineExceeded },
		slog.Default(), time.Hour, time.Hour)

	d.Trigger()
	require.NotPanics(t, d.Flush)
}
