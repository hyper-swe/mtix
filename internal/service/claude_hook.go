// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// ClaudeHookEvent names a Claude Code hook event that `mtix hook claude-session`
// handles (MTIX-112.40.4, ADR-007 route B, hook-waiter).
type ClaudeHookEvent string

const (
	// ClaudeHookSessionStart registers the agent and arms its waiter.
	ClaudeHookSessionStart ClaudeHookEvent = "session-start"
	// ClaudeHookStop arms the waiter again at the end of a turn.
	ClaudeHookStop ClaudeHookEvent = "stop"
)

// ClaudeHookExitWake is the exit code a hook-run waiter uses to wake an idle
// Claude session: the harness reads stderr as the wake text (asyncRewake).
const ClaudeHookExitWake = 2

// Outcomes of one hook run. Every run reports exactly one.
const (
	ClaudeHookWoke         = "woke"
	ClaudeHookEmpty        = "empty"
	ClaudeHookAlreadyArmed = "already-armed"
	ClaudeHookClaimLost    = "claim-lost"
	ClaudeHookHeld         = "held"
	ClaudeHookFailed       = "error"
)

// ClaudeHookLeases is the single-instance boundary: one holder per registration.
type ClaudeHookLeases interface {
	Acquire(ctx context.Context, key WakeLeaseKey, ttl time.Duration) (WakeLease, error)
	Release(ctx context.Context, key WakeLeaseKey, holder string, ttl time.Duration) (WakeLeaseReleaseResult, error)
}

// ClaudeHookClaims is the wake-ledger boundary: one claim per registration and event.
type ClaudeHookClaims interface {
	ClaimWake(ctx context.Context, key model.WakeKey, lease time.Duration) (model.WakeAttempt, bool, error)
	RecordWakeOutcome(ctx context.Context, attempt model.WakeAttempt, outcome model.WakeOutcome) error
}

// ClaudeHookDeps are the injected collaborators of ClaudeHook.
type ClaudeHookDeps struct {
	Key        WakeLeaseKey
	Register   func(ctx context.Context) error                                                   // session-start only
	Epoch      func(ctx context.Context) (int64, error)                                          // the registration epoch
	Wakeable   func(ctx context.Context) (bool, string, error)                                   // the wake policy (A12)
	Wait       func(ctx context.Context, skip func(seq int64) bool) ([]sqlite.InboxEvent, error) // inbox wait that ignores skipped events
	Render     func(events []sqlite.InboxEvent) (string, error)                                  // the quoted envelope
	Leases     ClaudeHookLeases
	Claims     ClaudeHookClaims
	Log        *ClaudeHookLog
	LeaseTTL   time.Duration
	ClaimLease time.Duration
}

// ClaudeHookResult is what the command prints and exits with.
type ClaudeHookResult struct {
	ExitCode int    // 0, or ClaudeHookExitWake
	Envelope string // stderr text when ExitCode is ClaudeHookExitWake
	Outcome  string
	Detail   string // reason for held, or the recorded error text
	soft     error  // a failure that did not stop the wake; run hands it on as relErr
}

// ClaudeHook runs the Claude session hooks as a service (FR-20, ADR-007 R2/R3).
type ClaudeHook struct{ d ClaudeHookDeps }

// NewClaudeHook builds a ClaudeHook from its dependencies.
func NewClaudeHook(d ClaudeHookDeps) *ClaudeHook { return &ClaudeHook{d: d} }

// Run handles one hook event. It never returns an error and never panics out: a
// hook must not break a session, so an internal failure is recorded for doctor
// (wake.listener.hook-errors) and the exit code stays 0 (MTIX-112.40.4).
func (h *ClaudeHook) Run(ctx context.Context, ev ClaudeHookEvent) (res ClaudeHookResult) {
	defer func() {
		if r := recover(); r != nil {
			res = h.failed(ev, fmt.Errorf("hook panic: %v", r))
		}
	}()
	res, relErr, err := h.run(ctx, ev)
	if err != nil {
		return h.failed(ev, errors.Join(err, relErr))
	}
	if relErr != nil {
		// The wake is already claimed and settled: it must still reach the session.
		// The lease expires by its TTL; doctor shows the failed release.
		if rerr := h.d.Log.Record(h.d.Key.AgentID, ev, relErr); rerr != nil {
			res.Detail = "record hook error: " + rerr.Error()
		}
		return res
	}
	if rerr := h.d.Log.Record(h.d.Key.AgentID, ev, nil); rerr != nil {
		res.Detail = "clear hook record: " + rerr.Error()
	}
	return res
}

func (h *ClaudeHook) failed(ev ClaudeHookEvent, err error) ClaudeHookResult {
	res := ClaudeHookResult{Outcome: ClaudeHookFailed, Detail: err.Error()}
	if rerr := h.d.Log.Record(h.d.Key.AgentID, ev, err); rerr != nil {
		res.Detail += "; record hook error: " + rerr.Error()
	}
	return res
}

// run registers (session-start), applies the wake policy, takes the single
// waiter lease, waits, claims each event once and renders the envelope. The
// lease is released on every path, a panic included; a failed release is
// returned apart from err so it cannot cancel a wake already settled.
func (h *ClaudeHook) run(ctx context.Context, ev ClaudeHookEvent) (res ClaudeHookResult, relErr, err error) {
	switch ev {
	case ClaudeHookSessionStart:
		if rerr := h.d.Register(ctx); rerr != nil {
			return res, nil, fmt.Errorf("register agent: %w", rerr)
		}
	case ClaudeHookStop:
	default:
		return res, nil, fmt.Errorf("unknown hook event %q: %w", ev, model.ErrInvalidInput)
	}
	ok, reason, err := h.d.Wakeable(ctx)
	if err != nil {
		return res, nil, fmt.Errorf("wake policy: %w", err)
	}
	if !ok {
		return ClaudeHookResult{Outcome: ClaudeHookHeld, Detail: reason}, nil, nil
	}
	lease, err := h.d.Leases.Acquire(ctx, h.d.Key, h.d.LeaseTTL)
	if errors.Is(err, model.ErrAlreadyClaimed) {
		return ClaudeHookResult{Outcome: ClaudeHookAlreadyArmed}, nil, nil
	}
	if err != nil {
		return res, nil, fmt.Errorf("take waiter lease: %w", err)
	}
	defer func() {
		if _, rerr := h.d.Leases.Release(context.WithoutCancel(ctx), h.d.Key, lease.Holder, h.d.LeaseTTL); rerr != nil {
			relErr = errors.Join(relErr, fmt.Errorf("release waiter lease: %w", rerr))
		}
	}()
	res, err = h.wakeOnce(ctx)
	relErr, res.soft = res.soft, nil
	return res, relErr, err
}

// wakeOnce waits for events, claims each once in the wake ledger and turns the
// claimed ones into the exit-2 envelope. An event another route (or an earlier
// run of this registration) already claimed is skipped and does not end the
// wait, so a re-arm blocks until NEW mail arrives and one event wakes the
// session once. res.soft is a failure that did not stop the wake.
func (h *ClaudeHook) wakeOnce(ctx context.Context) (ClaudeHookResult, error) {
	seen := map[int64]bool{}
	skip := func(seq int64) bool { return seen[seq] }
	for {
		events, err := h.d.Wait(ctx, skip)
		if err != nil {
			return ClaudeHookResult{}, fmt.Errorf("wait for inbox: %w", err)
		}
		if len(events) == 0 {
			return ClaudeHookResult{Outcome: ClaudeHookEmpty}, nil
		}
		fresh := unseenEvents(events, seen)
		if len(fresh) == 0 {
			return ClaudeHookResult{Outcome: ClaudeHookClaimLost}, nil // a Wait that ignored skip must not spin
		}
		epoch, err := h.d.Epoch(ctx)
		if err != nil {
			return ClaudeHookResult{}, fmt.Errorf("read registration epoch: %w", err)
		}
		won, attempts, cerr := h.claimEvents(ctx, fresh, epoch, seen)
		if len(won) > 0 {
			res, derr := h.deliver(ctx, won, attempts)
			res.soft = errors.Join(res.soft, cerr) // a claim failure must not cost the wake already won
			return res, derr
		}
		if cerr != nil {
			return ClaudeHookResult{}, cerr
		}
	}
}

func unseenEvents(events []sqlite.InboxEvent, seen map[int64]bool) []sqlite.InboxEvent {
	var fresh []sqlite.InboxEvent
	for _, e := range events {
		if !seen[e.Seq] {
			fresh = append(fresh, e)
		}
	}
	return fresh
}

// claimEvents claims each event once; a lost claim is remembered in seen. On a
// claim error it stops and returns the claims won so far, which the caller delivers.
func (h *ClaudeHook) claimEvents(ctx context.Context, events []sqlite.InboxEvent, epoch int64, seen map[int64]bool) ([]sqlite.InboxEvent, []model.WakeAttempt, error) {
	var won []sqlite.InboxEvent
	var attempts []model.WakeAttempt
	for _, e := range events {
		key := model.WakeKey{AgentID: h.d.Key.AgentID, HostID: h.d.Key.HostID, UID: h.d.Key.UID, EventSeq: e.Seq, Epoch: epoch}
		attempt, claimed, cerr := h.d.Claims.ClaimWake(ctx, key, h.d.ClaimLease)
		if cerr != nil {
			return won, attempts, fmt.Errorf("claim wake for event %d: %w", e.Seq, cerr)
		}
		if !claimed {
			seen[e.Seq] = true
			continue
		}
		won = append(won, e)
		attempts = append(attempts, attempt)
	}
	return won, attempts, nil
}

// deliver renders the claimed events and settles them. A failed outcome write
// does not cancel the wake: the envelope is still returned and the failure is
// the soft error, logged for doctor (the claim lease expires by itself).
func (h *ClaudeHook) deliver(ctx context.Context, won []sqlite.InboxEvent, attempts []model.WakeAttempt) (ClaudeHookResult, error) {
	text, err := h.d.Render(won)
	if err != nil {
		return ClaudeHookResult{}, errors.Join(fmt.Errorf("render envelope: %w", err), h.settle(ctx, attempts, model.WakeError))
	}
	soft := h.settle(ctx, attempts, model.WakeDelivered)
	return ClaudeHookResult{ExitCode: ClaudeHookExitWake, Envelope: text, Outcome: ClaudeHookWoke, soft: soft}, nil
}

// settle records the terminal outcome of every claim this run holds.
func (h *ClaudeHook) settle(ctx context.Context, attempts []model.WakeAttempt, outcome model.WakeOutcome) error {
	var errs []error
	for _, a := range attempts {
		if err := h.d.Claims.RecordWakeOutcome(context.WithoutCancel(ctx), a, outcome); err != nil {
			errs = append(errs, fmt.Errorf("record wake outcome for event %d: %w", a.Key.EventSeq, err))
		}
	}
	return errors.Join(errs...)
}
