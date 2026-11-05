// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// refireStaleClaims is the floor-independent crash-recovery leg (FR-20 §7).
// Claim identities are captured by the scan and guarded again in the writer
// transaction, so recovery cannot mistake a reused rowid for the crashed event.
func (d *HooksDispatcher) refireStaleClaims(ctx context.Context, cfg hooks.Config, execTrusted map[string]hooks.Decision, execMode string) {
	claims, err := d.store.StaleHookClaims(ctx, hookClaimLease)
	if err != nil {
		d.logger.Error("hook dispatch: stale-claim scan", "error", err)
		return
	}
	for _, claim := range claims {
		d.refireStaleClaim(ctx, cfg, claim, execTrusted, execMode)
	}
}

func (d *HooksDispatcher) refireStaleClaim(ctx context.Context, cfg hooks.Config, claim sqlite.HookClaim, execTrusted map[string]hooks.Decision, execMode string) {
	expected := sqlite.JournalEvent{Seq: claim.Seq, EventID: claim.EventID}
	won, matched, err := d.store.ClaimHookDispatchEvent(ctx, claim.Hook, expected, hookClaimLease)
	if err != nil {
		d.logger.Error("hook dispatch: reclaim", "hook", claim.Hook, "seq", claim.Seq, "error", err)
		return
	}
	if !matched {
		d.identityFenceMisses.Add(1)
	}
	if !won {
		return
	}
	h, found := hookByName(cfg, claim.Hook)
	event, ok, err := d.store.ReadJournalEventAt(ctx, claim.Seq)
	if err == nil && (!ok || event.EventID != claim.EventID) {
		d.identityFenceMisses.Add(1)
		return
	}
	evt := NormalizeEvent(event)
	if err != nil || !ok || !found || evt.Name == "" {
		d.logger.Warn("hook dispatch: abandoning stale claim — hook or event no longer resolvable", "hook", claim.Hook, "seq", claim.Seq, "error", err)
		d.recordOutcome(ctx, claim.Hook, expected, sqlite.OutcomeError)
		d.logFiring(ctx, hooks.Hook{Name: claim.Hook}, evt, "", sqlite.OutcomeError, "stale claim abandoned")
		return
	}
	d.logger.Info("hook dispatch: re-firing stale claim (crashed trigger)", "hook", claim.Hook, "seq", claim.Seq)
	d.finishClaim(ctx, h, evt, event, execTrusted, execMode)
}
