// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// scanRegistration runs the per-registration steps of ADR-007 section 2.7: enumerate the
// events above the mark, apply the gates, then hand the dispatcher EVERY event above the
// mark (never a subset), so no pass can omit a lower event and advance past it.
func (s *WakeScanner) scanRegistration(ctx context.Context, reg *sqlite.Registration) error {
	keys, skip, err := s.candidates(ctx, reg)
	if err != nil || len(keys) == 0 {
		return err
	}
	if len(skip) < len(keys) { // something foreign to wake: the gates apply
		admitted, gateErr := s.admit(ctx, reg, keys[0])
		if gateErr != nil || !admitted {
			return gateErr
		}
	}
	return s.opts.Dispatcher.PassSkipping(ctx, keys, skip)
}

// candidates lists every unacknowledged addressed event above the registration's own mark,
// ascending, and the subset the recipient authored (those never wake it).
func (s *WakeScanner) candidates(ctx context.Context, reg *sqlite.Registration) ([]model.WakeKey, map[model.WakeKey]bool, error) {
	events, err := s.opts.Store.InboxList(ctx, reg.AgentID)
	if err != nil {
		return nil, nil, fmt.Errorf("list inbox for wake scan %q: %w", reg.AgentID, err)
	}
	var keys []model.WakeKey
	skip := map[model.WakeKey]bool{}
	for _, ev := range events {
		if ev.Seq <= reg.HighWater {
			continue
		}
		key := model.WakeKey{AgentID: reg.AgentID, HostID: reg.HostID, UID: reg.UID, EventSeq: ev.Seq, Epoch: reg.Epoch}
		keys = append(keys, key)
		if ev.Author == reg.AgentID {
			skip[key] = true
		}
	}
	return keys, skip, nil
}

// admit applies the scanner-owned gates in order: the grant (Wakeable), the session's
// liveness (a dead row is marked stale and nothing is passed; unknown counts as live) and a
// live route. Each refusal leaves the events above the mark, claimed by nobody.
func (s *WakeScanner) admit(ctx context.Context, reg *sqlite.Registration, first model.WakeKey) (bool, error) {
	ok, reason, err := s.opts.Wakeable(ctx, reg.AgentID)
	if err != nil {
		return false, fmt.Errorf("check wakeable for %q: %w", reg.AgentID, err)
	}
	if !ok {
		s.opts.Logger.Debug("wake scan: agent not wakeable", "agent", reg.AgentID, "reason", reason)
		return false, nil
	}
	if s.opts.Liveness.Probe(ctx, *reg) == LiveDead {
		return false, s.markStale(ctx, reg)
	}
	if !s.opts.Dispatcher.HasLiveRoute(ctx, first) {
		s.opts.Logger.Debug("wake scan: no live route; claiming nothing", "agent", reg.AgentID)
		return false, nil
	}
	return true, nil
}

// markStale records the first time the scanner saw the session dead (step 4). The event
// stays in the inbox: a dead session claims nothing and consumes nothing.
func (s *WakeScanner) markStale(ctx context.Context, reg *sqlite.Registration) error {
	key := sqlite.RegistrationKey{AgentID: reg.AgentID, HostID: reg.HostID, UID: reg.UID}
	if _, err := s.opts.Store.MarkRegistrationStale(ctx, key, reg.Epoch, s.opts.Clock().UTC().Format("2006-01-02T15:04:05Z")); err != nil {
		return fmt.Errorf("mark registration stale: %w", err)
	}
	return nil
}
