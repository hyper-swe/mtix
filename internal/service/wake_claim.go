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

// NewWakeClaimer builds the claim function over a wake ledger. evidence may be nil.
func NewWakeClaimer(ledger WakeClaimLedger, lease time.Duration, evidence WakeEvidence) (*WakeClaimer, error) {
	if ledger == nil || lease <= 0 {
		return nil, fmt.Errorf("wake claimer needs a ledger and a positive lease: %w", model.ErrInvalidInput)
	}
	return &WakeClaimer{ledger: ledger, lease: lease, evidence: evidence, logger: slog.Default()}, nil
}

// WithLogger sets the logger that reports a failed delivery-evidence read (MTIX-112.40.13.3).
// A nil logger keeps the current one. It returns the claimer for chaining.
func (c *WakeClaimer) WithLogger(logger *slog.Logger) *WakeClaimer {
	if logger != nil {
		c.logger = logger
	}
	return c
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
