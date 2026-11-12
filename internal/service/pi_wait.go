// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// PiWaitStore is the board capability the pi wait child needs.
type PiWaitStore interface {
	ListRegistrations(ctx context.Context, hostID, uid string) ([]sqlite.Registration, error)
	InboxList(ctx context.Context, agentID string) ([]sqlite.InboxEvent, error)
	ClaimWake(ctx context.Context, key model.WakeKey, lease time.Duration) (model.WakeAttempt, bool, error)
	RecordWakeResult(ctx context.Context, attempt model.WakeAttempt, r model.WakeResult) error
	UpgradeWakeConfirmed(ctx context.Context, key model.WakeKey) error
	InboxEventAcked(ctx context.Context, agent string, seq int64) (bool, error)
}

// PiWaitOptions injects every capability of the pi wait adapter.
type PiWaitOptions struct {
	Open  func(ctx context.Context) (PiWaitStore, func() error, error)
	Host  func() (hostID, uid string, err error)
	Lease time.Duration
	Wait  func(ctx context.Context) error
}

// PiWait is the attach adapter of the pi harness (MTIX-112.19.7, ADR-007 section 2.7).
// Its wait verb is the scanner for its own registration: it claims through the single
// WakeClaimer over the same wake ledger as the host scanner (which skips pi rows), so one event produces one wake and a
// claimed event never wakes again, acknowledged or not. Run returns nil only after a
// successful claim, a terminal ledger row and one wake line on stdout.
type PiWait struct{ opts PiWaitOptions }

// NewPiWait validates the injected capabilities.
func NewPiWait(opts PiWaitOptions) (*PiWait, error) {
	if opts.Open == nil || opts.Host == nil || opts.Wait == nil || opts.Lease <= 0 {
		return nil, fmt.Errorf("pi wait capabilities are required: %w", model.ErrInvalidInput)
	}
	return &PiWait{opts: opts}, nil
}

var _ attach.Adapter = (*PiWait)(nil)

// Run performs one attach verb. Only wait does work here: session-start, prompt-submit and
// session-end are the extension's own business for pi. A lost race, a claim error or a
// cancelled context returns an error and prints nothing.
func (p *PiWait) Run(ctx context.Context, req attach.VerbRequest) error {
	if req.Verb != attach.VerbWait {
		return nil
	}
	if err := attach.ValidateSession(req.Session); err != nil {
		return fmt.Errorf("pi wait: %w", err)
	}
	hostID, uid, err := p.opts.Host()
	if err != nil {
		return fmt.Errorf("pi wait host identity: %w", err)
	}
	st, closeStore, err := p.opts.Open(ctx)
	if err != nil {
		return fmt.Errorf("pi wait open board: %w", err)
	}
	defer func() { _ = closeStore() }() //nolint:errcheck // a close failure cannot change a finished wait
	for {
		attempt, ok, err := p.tryClaim(ctx, st, hostID, uid, req.Session)
		if err != nil {
			return err
		}
		if ok {
			return p.deliver(ctx, st, attempt, req.Stdout)
		}
		if err := p.opts.Wait(ctx); err != nil {
			return fmt.Errorf("pi wait stopped: %w", err)
		}
	}
}

// tryClaim claims the lowest unacknowledged foreign event above the registration's mark
// that no one has claimed. An event with a ledger row, terminal or leased, is skipped.
// It is the scanner for its own registration, the same role as the host scanner, so
// its reads are internal: they stamp no last read and register no waiter (MTIX-112.3).
func (p *PiWait) tryClaim(ctx context.Context, st PiWaitStore, hostID, uid, session string) (model.WakeAttempt, bool, error) {
	ctx = sqlite.WithoutReadStamp(ctx)
	regs, err := st.ListRegistrations(ctx, hostID, uid)
	if err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("pi wait list registrations: %w", err)
	}
	claimer, err := NewWakeClaimer(st, p.opts.Lease, defaultWakeEvidence(nil, st))
	if err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("pi wait claim function: %w", err)
	}
	for i := range regs {
		reg := &regs[i]
		if reg.Harness != harnessPi || reg.SessionHandle != session {
			continue
		}
		events, err := st.InboxList(ctx, reg.AgentID)
		if err != nil {
			return model.WakeAttempt{}, false, fmt.Errorf("pi wait list inbox: %w", err)
		}
		for _, ev := range events {
			if ev.Seq <= reg.HighWater || ev.Author == reg.AgentID {
				continue
			}
			key := model.NewWakeKey(model.WakeRecipient{AgentID: reg.AgentID, HostID: reg.HostID, UID: reg.UID}, ev.Seq, reg.Epoch, ev.EventID)
			attempt, claimed, err := claimer.Claim(ctx, key) // the one claim function, so a reclaim is evidence-checked
			if err != nil {
				return model.WakeAttempt{}, false, fmt.Errorf("pi wait claim event %d: %w", ev.Seq, err)
			}
			if claimed {
				return attempt, true, nil
			}
		}
	}
	return model.WakeAttempt{}, false, nil
}

// piWakeLine is the one JSON line the extension reads; it carries no message content.
type piWakeLine struct {
	Event string `json:"event"`
	Seq   int64  `json:"seq"`
	Nudge string `json:"nudge"`
}

// deliver records the terminal outcome first and prints the wake line second, so a line on
// stdout always implies a terminal ledger row and the event can never be claimed again.
func (p *PiWait) deliver(ctx context.Context, st PiWaitStore, attempt model.WakeAttempt, out io.Writer) error {
	result := model.WakeResult{Outcome: model.WakeSent, Route: harnessPi}
	if err := st.RecordWakeResult(ctx, attempt, result); err != nil {
		return fmt.Errorf("pi wait record outcome for event %d: %w", attempt.Key.EventSeq, err)
	}
	line, err := json.Marshal(piWakeLine{Event: "wake", Seq: attempt.Key.EventSeq,
		Nudge: fmt.Sprintf("You have a new mtix inbox message (event %d). Read it with: mtix inbox", attempt.Key.EventSeq)})
	if err != nil {
		return fmt.Errorf("pi wait encode wake line: %w", err)
	}
	if _, err := fmt.Fprintf(out, "%s\n", line); err != nil {
		return fmt.Errorf("pi wait write wake line: %w", err)
	}
	return nil
}
