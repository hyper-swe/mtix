// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "context"

// WakeScanTrigger is the wake pass a dispatcher runs after its hook pass (FR-20,
// ADR-007 section 2.7). *WakeScanner implements it.
type WakeScanTrigger interface {
	Scan(ctx context.Context) error
}

// WithWakeScanner makes every Dispatch end with a wake scan, so each trigger that already
// runs the dispatcher (the CLI and MCP post-command callback, the host dispatcher tick)
// also wakes sleeping recipients. The scan is independent of the hook ledger and of the
// exec-dispatch policy: its own claim ledger keeps concurrent triggers to one wake per
// (registration, event). Safe on a nil dispatcher.
func (d *HooksDispatcher) WithWakeScanner(s WakeScanTrigger) *HooksDispatcher {
	if d == nil {
		return nil
	}
	d.wake = s
	return d
}

// scanWake runs the wake scan. Like the hook pass it never fails the caller's mutation:
// an error is logged and nothing else.
func (d *HooksDispatcher) scanWake(ctx context.Context) {
	if d.wake == nil {
		return
	}
	if err := d.wake.Scan(ctx); err != nil {
		d.logger.Warn("wake scan failed", "error", err)
	}
}
