// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"strconv"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// WakeSettingsReader supplies effective host policy without mutation (FR-20.3).
type WakeSettingsReader interface {
	WakeSettings(string) (map[string]model.WakeSetting, []oplocal.Report, error)
}

// wakeVerdict is the guard's decision for one candidate.
type wakeVerdict int

const (
	verdictSend wakeVerdict = iota // eligible and within every guard
	verdictHeld                    // a loop guard tripped; the event waits and blocks later ones
	verdictSkip                    // not eligible (at or below the mark, stale epoch, blocked) or unreadable
)

// guardWakeHop decides one candidate: first the registration's own high-water re-arm
// rule (I10, ADR-007 section 5.4), then the hop-depth guard. Any read error is a skip
// that the caller turns into a block, so the mark never moves over an unjudged event.
func (d *WakeDispatcher) guardWakeHop(ctx context.Context, key model.WakeKey, st *passState) (int64, wakeVerdict, error) {
	ok, err := d.eligible(ctx, key, st) // re-arm from this registration's own mark, never another's
	if err != nil || !ok {
		return 0, verdictSkip, err
	}
	hop, err := d.store.WakeEventHop(ctx, key)
	if err != nil {
		return 0, verdictSkip, fmt.Errorf("read candidate chain depth: %w", err)
	}
	if hop < 0 {
		return 0, verdictSkip, fmt.Errorf("negative candidate hop: %w", model.ErrInvalidInput)
	}
	limit, err := d.hopLimit(key.AgentID)
	if err != nil {
		return 0, verdictSkip, err
	}
	if hop <= limit {
		return hop, verdictSend, nil
	}
	fix := "mtix config set wake.max_hop_depth " + strconv.FormatInt(hop, 10)
	if d.settings != nil {
		fix += " --agent " + key.AgentID
	}
	detail := fmt.Sprintf("held wake: hop depth %d exceeds %d [chain: event %d hop %d]; %s", hop, limit, key.EventSeq, hop, fix)
	d.held(detail)
	_, err = d.alerts.Raise(ctx, AlertRequest{Agent: key.AgentID, Class: oplocal.AlertClassWakeHopDepth, Detail: detail, Fix: fix})
	if err != nil {
		return hop, verdictHeld, fmt.Errorf("record hop guard trip: %w", err)
	}
	return hop, verdictHeld, nil
}

// hopLimit reads the effective wake.max_hop_depth for the agent (default 32).
func (d *WakeDispatcher) hopLimit(agent string) (int64, error) {
	if d.settings == nil {
		return 32, nil
	}
	values, reports, err := d.settings.WakeSettings(agent)
	if err != nil {
		return 0, fmt.Errorf("read wake hop policy: %w", err)
	}
	if len(reports) > 0 {
		return 0, fmt.Errorf("foreign host wake settings: %w", model.ErrOperatorStateUnreadable)
	}
	limit, err := strconv.ParseInt(values["wake.max_hop_depth"].Value, 10, 64)
	if err != nil || limit <= 0 {
		return 0, fmt.Errorf("hop guard must be positive: %w", model.ErrInvalidInput)
	}
	return limit, nil
}
