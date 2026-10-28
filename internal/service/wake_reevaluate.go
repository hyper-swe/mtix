// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeReevaluator re-checks held wakes after an activity that can lift a hold:
// a grant, a wake.* setting change or an inbox call (FR-20.3, ADR-007 section 5.4).
// It is injected into the service entry points and is nil by default, which makes
// the notification a no-op. It is activity-driven only: no timer or background loop.
type WakeReevaluator interface {
	// Reevaluate re-runs held candidates for agent; an empty agent means every agent.
	Reevaluate(ctx context.Context, agent string) error
}

// WakeHeldCandidates lists the unacknowledged events above each held registration's
// own high-water mark, for the agent (or every agent when empty).
type WakeHeldCandidates interface {
	HeldWakeCandidates(ctx context.Context, agent string) ([]model.WakeKey, error)
}

// Reevaluate runs the same guarded pass over the held registrations of agent, with the
// same own-mark, epoch and head-of-line rules as Pass (FR-20.3, I10, I14, I15).
func (d *WakeDispatcher) Reevaluate(ctx context.Context, agent string) error {
	source, ok := d.store.(WakeHeldCandidates)
	if !ok {
		return fmt.Errorf("wake store cannot list held candidates: %w", model.ErrInvalidInput)
	}
	if agent != "" {
		if err := validateAgentID(agent); err != nil {
			return fmt.Errorf("validate re-evaluation agent: %w", err)
		}
	}
	keys, err := source.HeldWakeCandidates(ctx, agent)
	if err != nil {
		return fmt.Errorf("list held wake candidates: %w", err)
	}
	if len(keys) == 0 {
		return nil
	}
	return d.Pass(ctx, keys)
}

// notifyReevaluator runs a re-evaluation after the caller's change has committed.
// It never fails or undoes that change: an error is logged and nothing else.
func notifyReevaluator(ctx context.Context, r WakeReevaluator, agent, trigger string) {
	if r == nil {
		return
	}
	if err := r.Reevaluate(ctx, agent); err != nil {
		slog.Warn("wake re-evaluation failed", "trigger", trigger, "agent", agent, "error", err)
	}
}
