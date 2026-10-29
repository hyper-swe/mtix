// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
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
// own high-water mark for this host and uid, for the agent (or every agent when empty).
type WakeHeldCandidates interface {
	HeldWakeCandidates(ctx context.Context, hostID, uid, agent string) ([]model.WakeKey, error)
}

// reevaluationScope carries what Reevaluate needs beyond Pass: the host scope and the
// scanner-owned candidate filter.
type reevaluationScope struct {
	host, uid string
	filter    func(context.Context, model.WakeKey) (bool, error)
}

// Reevaluate re-runs the held registrations of agent through the same guarded pass, with
// the same own-mark, epoch and head-of-line rules (FR-20.3, I10, I14, I15). It never sends
// what the scanner would refuse: it fails closed with ErrWakeFilterRequired unless the
// host scope and the scanner's candidate filter are injected, and it skips any agent whose
// effective wake.live is off or whom the filter rejects (ADR-007 section 2.7).
func (d *WakeDispatcher) Reevaluate(ctx context.Context, agent string) error {
	// Re-evaluation never carries a session identity: reading inboxes must not refresh last_seen.
	ctx = sqlite.WithSessionTouch(ctx, sqlite.SessionTouch{})
	source, ok := d.store.(WakeHeldCandidates)
	if !ok {
		return fmt.Errorf("wake store cannot list held candidates: %w", model.ErrInvalidInput)
	}
	if d.reeval.filter == nil || d.reeval.host == "" || d.reeval.uid == "" {
		return fmt.Errorf("re-evaluation needs the host scope and candidate filter: %w", model.ErrWakeFilterRequired)
	}
	if agent != "" {
		if err := validateAgentID(agent); err != nil {
			return fmt.Errorf("validate re-evaluation agent: %w", err)
		}
	}
	keys, err := source.HeldWakeCandidates(ctx, d.reeval.host, d.reeval.uid, agent)
	if err != nil {
		return fmt.Errorf("list held wake candidates: %w", err)
	}
	allowed, gateErr := d.admitRegistrations(ctx, keys)
	if len(allowed) == 0 {
		return gateErr
	}
	allowed, skip, skipErr := d.ownEvents(ctx, allowed)
	if skipErr != nil {
		return errors.Join(gateErr, skipErr) // fail closed: an unjudged author could wake an agent for its own event
	}
	return errors.Join(gateErr, d.PassSkipping(ctx, allowed, skip))
}

// WakeInboxReader lists the events addressed to an agent with their authors.
type WakeInboxReader interface {
	InboxList(ctx context.Context, agentID string) ([]sqlite.InboxEvent, error)
}

// ownEvents returns the candidates to pass and the ones the recipient authored itself:
// re-evaluation covers those without a send, exactly as a scan does, so no path wakes an
// agent for its own event. An event whose author cannot be determined (it was acknowledged
// since the candidate list was taken) is neither sent nor covered: the pass is cut at it,
// together with every higher event of that registration, so the mark never moves past an
// unsent event. The next pass retries from the inbox as it then stands.
func (d *WakeDispatcher) ownEvents(ctx context.Context, keys []model.WakeKey) ([]model.WakeKey, map[model.WakeKey]bool, error) {
	reader, ok := d.store.(WakeInboxReader)
	if !ok {
		return nil, nil, fmt.Errorf("wake store cannot read event authors: %w", model.ErrInvalidInput)
	}
	authors := map[string]map[int64]string{}
	cutAt := map[string]int64{}
	skip := map[model.WakeKey]bool{}
	for _, key := range keys {
		byseq, err := authorsOf(ctx, reader, authors, key.AgentID)
		if err != nil {
			return nil, nil, err
		}
		author, known := byseq[key.EventSeq]
		if low, cut := cutAt[key.AgentID]; !known && (!cut || key.EventSeq < low) {
			cutAt[key.AgentID] = key.EventSeq
		}
		if known && author == key.AgentID {
			skip[key] = true
		}
	}
	kept := make([]model.WakeKey, 0, len(keys))
	for _, key := range keys {
		if low, cut := cutAt[key.AgentID]; cut && key.EventSeq >= low {
			continue
		}
		kept = append(kept, key)
	}
	return kept, skip, nil
}

// admitRegistrations keeps only the candidates of registrations that pass the live-wake
// switch and the scanner filter. A registration is judged once; an error drops it.
func (d *WakeDispatcher) admitRegistrations(ctx context.Context, keys []model.WakeKey) ([]model.WakeKey, error) {
	verdict := map[string]bool{}
	var failures error
	var kept []model.WakeKey
	for _, key := range keys {
		id := holdRegistrationID(key)
		admitted, seen := verdict[id]
		if !seen {
			var err error
			admitted, err = d.admitRegistration(ctx, key)
			if err != nil {
				failures = errors.Join(failures, err)
			}
			verdict[id] = admitted
		}
		if admitted {
			kept = append(kept, key)
		}
	}
	return kept, failures
}

func (d *WakeDispatcher) admitRegistration(ctx context.Context, key model.WakeKey) (bool, error) {
	values, reports, err := d.budgets.settings.WakeSettings(key.AgentID)
	if err != nil {
		return false, fmt.Errorf("read live wake switch: %w", err)
	}
	if len(reports) > 0 {
		return false, fmt.Errorf("foreign host wake settings: %w", model.ErrOperatorStateUnreadable)
	}
	if values["wake.live"].Value == "false" {
		return false, nil
	}
	ok, err := d.reeval.filter(ctx, key)
	if err != nil {
		return false, fmt.Errorf("filter held wake candidate: %w", err)
	}
	return ok, nil
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

// authorsOf returns the author of every inbox event of the agent by seq, reading the inbox
// once per agent and caching the result.
func authorsOf(ctx context.Context, reader WakeInboxReader, cache map[string]map[int64]string, agent string) (map[int64]string, error) {
	if byseq, seen := cache[agent]; seen {
		return byseq, nil
	}
	events, err := reader.InboxList(ctx, agent)
	if err != nil {
		return nil, fmt.Errorf("read event authors for %q: %w", agent, err)
	}
	byseq := make(map[int64]string, len(events))
	for _, ev := range events {
		byseq[ev.Seq] = ev.Author
	}
	cache[agent] = byseq
	return byseq, nil
}
