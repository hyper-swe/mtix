// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeEvidence reports whether the target session was already observed acting on the event
// of a key, for example through a confirmation stamp. nil means no evidence is available.
type WakeEvidence func(ctx context.Context, key model.WakeKey) (bool, error)

// WakeClaimLedger is the wake ledger capability the single claim function needs.
type WakeClaimLedger interface {
	ClaimWake(ctx context.Context, key model.WakeKey, lease time.Duration) (model.WakeAttempt, bool, error)
	RecordWakeResult(ctx context.Context, attempt model.WakeAttempt, r model.WakeResult) error
	UpgradeWakeConfirmed(ctx context.Context, key model.WakeKey) error
}

// WakeClaimer is the one claim function for an addressed event (MTIX-112.40.13, FR-20,
// ADR-007 one claim per registration and event). Every delivery route claims through it,
// and so must any retry or stall recovery (MTIX-112.40.12): a retry that goes around it
// can wake a session twice.
//
// The claim is one ledger write transaction, taken before anything is pushed. A second
// route sees the claim and skips. A sent event is never claimed again. An expired claim
// that was never finished (the claimant crashed before it recorded a result) is reclaimed
// at most once; the reclaim is marked reclaimed-after-crash, and when delivery evidence
// shows the first push landed it is settled as confirmed instead of waking again.
type WakeClaimer struct {
	ledger   WakeClaimLedger
	lease    time.Duration
	evidence WakeEvidence
	logger   *slog.Logger
}

// NewWakeClaimer builds the claim function over a wake ledger. evidence may be nil. logger
// receives the warning for a failed evidence read (MTIX-112.40.13.3); nil falls back to the
// process default, the package's usual constructor fallback.
func NewWakeClaimer(ledger WakeClaimLedger, lease time.Duration, evidence WakeEvidence, logger *slog.Logger) (*WakeClaimer, error) {
	if ledger == nil || lease <= 0 {
		return nil, fmt.Errorf("wake claimer needs a ledger and a positive lease: %w", model.ErrInvalidInput)
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &WakeClaimer{ledger: ledger, lease: lease, evidence: evidence, logger: logger}, nil
}

// Claim claims the event once. The bool is true only when the caller must deliver it.
func (c *WakeClaimer) Claim(ctx context.Context, key model.WakeKey) (model.WakeAttempt, bool, error) {
	attempt, won, err := c.ledger.ClaimWake(ctx, key, c.lease)
	if err != nil || !won {
		return model.WakeAttempt{}, false, err
	}
	return c.Resolve(ctx, attempt)
}

// Resolve finishes a claim another path already won (the dispatcher claims through its
// budget). A first claim is delivered as is. A reclaim is checked against the delivery
// evidence while the fresh fence is held, so no other claimant can act in between.
func (c *WakeClaimer) Resolve(ctx context.Context, attempt model.WakeAttempt) (model.WakeAttempt, bool, error) {
	if !attempt.Reclaimed || c.evidence == nil {
		return attempt, true, nil
	}
	seen, err := c.evidence(ctx, attempt.Key)
	if err != nil {
		// Unknown evidence counts as none: the event is delivered again, never lost. The
		// read error is reported so a broken evidence source is visible (MTIX-112.40.13.3).
		c.logger.WarnContext(ctx, "wake delivery evidence read failed; redelivering",
			"agent_id", attempt.Key.AgentID, "host_id", attempt.Key.HostID, "uid", attempt.Key.UID,
			"event_seq", attempt.Key.EventSeq, "epoch", attempt.Key.Epoch, "error", err)
		return attempt, true, nil
	}
	if !seen {
		return attempt, true, nil
	}
	result := model.WakeResult{Outcome: model.WakeSent, Reason: model.WakeReasonReclaimedAfterCrash}
	if err := c.ledger.RecordWakeResult(context.WithoutCancel(ctx), attempt, result); err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("settle reclaimed wake as already delivered: %w", err)
	}
	if err := c.ledger.UpgradeWakeConfirmed(context.WithoutCancel(ctx), attempt.Key); err != nil {
		return model.WakeAttempt{}, false, fmt.Errorf("confirm reclaimed wake: %w", err)
	}
	return model.WakeAttempt{}, false, nil
}

// WakeAdmitFunc is the budget admission a claim is written under: it sees the proposed
// fenced claim and decides whether it may be written (ClaimWakeBudget).
type WakeAdmitFunc = func(model.WakeBudgetProposal) (model.WakeBudgetDecision, error)

// WakeStallLedger is the ledger capability stalled-turn recovery needs on top of the claim
// ledger: the one reopen of a sent, unconfirmed event under a budget admission (MTIX-112.40.12).
type WakeStallLedger interface {
	ReopenStalledWake(ctx context.Context, key model.WakeKey, window time.Duration, admit WakeAdmitFunc) (model.WakeBudgetResult, error)
}

// StalledAdmission runs the recipient's budget and back-off admission around a reopen. The
// dispatcher supplies it, so a recovery obeys the same gates as a first wake; it returns the
// won attempt, or false when the budget held it or another claimant owns it.
type StalledAdmission func(ctx context.Context, key model.WakeKey, reopen func(WakeAdmitFunc) (model.WakeBudgetResult, error)) (model.WakeAttempt, bool, error)

// ReclaimStalled takes the one permitted redelivery of an event whose push went unconfirmed
// because the session's turn never ended (MTIX-112.40.12, FR-20). It is the same claim
// function as Claim, never a second one: the ledger reopens the sent row once under a fresh
// fence (sent more than window ago, retry cap one) inside the admission the caller supplies,
// and Resolve then checks the delivery evidence while that fence is held, so an event the
// session already read is settled as confirmed and not pushed again. The bool is true only
// when the caller must deliver.
func (c *WakeClaimer) ReclaimStalled(ctx context.Context, key model.WakeKey, window time.Duration, admission StalledAdmission) (model.WakeAttempt, bool, error) {
	ledger, ok := c.ledger.(WakeStallLedger)
	if !ok || admission == nil {
		return model.WakeAttempt{}, false, fmt.Errorf("wake ledger cannot reopen a stalled event: %w", model.ErrInvalidInput)
	}
	attempt, won, err := admission(ctx, key, func(admit WakeAdmitFunc) (model.WakeBudgetResult, error) {
		return ledger.ReopenStalledWake(ctx, key, window, admit)
	})
	if err != nil || !won {
		return model.WakeAttempt{}, false, err
	}
	return c.Resolve(ctx, attempt)
}
