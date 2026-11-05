// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

var errHookDeliveryReplaced = errors.New("hook delivery journal identity changed")

// outcomeHookDeliveryReplaced is a private adapter aggregate, never a ledger
// outcome. The adapter already counted the quiet identity miss (FR-20).
const outcomeHookDeliveryReplaced = "skipped-replaced"

// HookIdentityFenceMisses counts writes suppressed because replace changed a
// journal identity during this dispatcher's lifetime (MTIX-112.2.2). These
// misses are expected concurrency outcomes, not failed delivery attempts.
func (d *HooksDispatcher) HookIdentityFenceMisses() uint64 {
	return d.identityFenceMisses.Load()
}

func (d *HooksDispatcher) advanceHookSnapshot(ctx context.Context, floor int64, events []sqlite.JournalEvent) {
	misses, err := d.store.AdvanceHookScanFloorForEvents(ctx, floor, events)
	if err != nil {
		d.logger.Error("hook dispatch: advance scan floor", "error", err)
		return
	}
	d.identityFenceMisses.Add(misses)
}

func (d *HooksDispatcher) recordOutcome(ctx context.Context, hook string, event sqlite.JournalEvent, outcome string) {
	// A replacement-only aggregate makes no outcome write; it is not a new
	// terminal state and the adapter already counted its identity miss.
	if outcome == outcomeHookDeliveryReplaced {
		return
	}
	matched, err := d.store.RecordHookDispatchEventOutcome(ctx, hook, event, outcome)
	if err != nil {
		d.logger.Error("hook dispatch: record outcome", "hook", hook, "seq", event.Seq, "error", err)
		return
	}
	if !matched {
		d.identityFenceMisses.Add(1)
	}
}

// hookDeliveryOutcome retains errors and policy skips when an adapter also
// observes a replaced event; a replacement alone is a quiet terminal skip.
func hookDeliveryOutcome(failed, untrusted, policy, replaced bool) string {
	switch {
	case failed:
		return sqlite.OutcomeError
	case untrusted:
		return sqlite.OutcomeSkippedUntrusted
	case policy:
		return sqlite.OutcomeSkippedPolicy
	case replaced:
		return outcomeHookDeliveryReplaced
	default:
		return sqlite.OutcomeDelivered
	}
}

// adapterFailureOutcome separates a quiet identity miss from delivery failures.
func (d *HooksDispatcher) adapterFailureOutcome(ctx context.Context, h hooks.Hook, event hooks.Event, name string, err error) string {
	if errors.Is(err, errHookDeliveryReplaced) {
		return outcomeHookDeliveryReplaced
	}
	if d.recordAdapterFailure(ctx, h, event, name, err) {
		return sqlite.OutcomeSkippedUntrusted
	}
	return sqlite.OutcomeError
}
