// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// ExportDebouncer coalesces auto-export requests from long-running
// interfaces (MCP server, HTTP serve) per FR-15.3 / MTIX-26.1.
//
// Every committed write transaction calls Trigger via the store's
// on-commit hook. The export itself runs on the trailing edge after
// quietPeriod without new triggers, capped at maxDelay from the first
// pending trigger so a sustained write burst cannot starve the mirror
// forever. Flush exports immediately if anything is pending; Close
// flushes and stops the debouncer — call it on interface shutdown so the
// final mutations always reach tasks.json.
type ExportDebouncer struct {
	exportFn    func(ctx context.Context) error
	logger      *slog.Logger
	quietPeriod time.Duration
	maxDelay    time.Duration

	mu         sync.Mutex
	timer      *time.Timer
	firstDirty time.Time // zero when nothing is pending
	closed     bool
}

// NewExportDebouncer wraps exportFn (normally SyncService.AutoExport bound
// to the project dir). quietPeriod and maxDelay of 0 select the defaults
// (1s quiet, 5s cap).
func NewExportDebouncer(
	exportFn func(ctx context.Context) error,
	logger *slog.Logger,
	quietPeriod, maxDelay time.Duration,
) *ExportDebouncer {
	if quietPeriod <= 0 {
		quietPeriod = time.Second
	}
	if maxDelay <= 0 {
		maxDelay = 5 * time.Second
	}
	return &ExportDebouncer{
		exportFn:    exportFn,
		logger:      logger,
		quietPeriod: quietPeriod,
		maxDelay:    maxDelay,
	}
}

// Trigger records a mutation and (re)arms the trailing-edge timer.
// Safe to call from any goroutine; never blocks on the export itself.
func (d *ExportDebouncer) Trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}

	now := time.Now()
	if d.firstDirty.IsZero() {
		d.firstDirty = now
	}

	delay := d.quietPeriod
	if remaining := d.maxDelay - now.Sub(d.firstDirty); remaining < delay {
		delay = remaining
	}
	if delay < 0 {
		delay = 0
	}

	if d.timer != nil {
		d.timer.Stop()
	}
	d.timer = time.AfterFunc(delay, d.fire)
}

// Flush exports immediately if a trigger is pending. Blocks until the
// export completes so callers (process shutdown) can rely on the mirror
// being current when it returns.
func (d *ExportDebouncer) Flush() {
	d.mu.Lock()
	if d.firstDirty.IsZero() {
		d.mu.Unlock()
		return
	}
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
	d.firstDirty = time.Time{}
	d.mu.Unlock()

	d.runExport()
}

// Close flushes any pending export and stops the debouncer permanently.
func (d *ExportDebouncer) Close() {
	d.mu.Lock()
	d.closed = true
	d.mu.Unlock()
	d.Flush()
}

// fire is the timer callback: clears pending state and runs the export.
func (d *ExportDebouncer) fire() {
	d.mu.Lock()
	d.timer = nil
	d.firstDirty = time.Time{}
	d.mu.Unlock()

	d.runExport()
}

// runExport executes the export. Failures are logged at error level —
// the mirror is the redundancy layer, so a failed export must be loud —
// but never propagate: mirror trouble must not take the interface down.
func (d *ExportDebouncer) runExport() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := d.exportFn(ctx); err != nil {
		d.logger.Error("auto-export failed — tasks.json mirror is stale",
			"error", err)
	}
}
