// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// wakeOutcome says what became of one claim-and-send.
type wakeOutcome int

const (
	wakeAttempted    wakeOutcome = iota // a send ran, or another claimant already owns the event
	wakeBudgetHeld                      // the recipient budget or back-off refused the claim
	wakeNotAttempted                    // a claim or fence error: nothing was sent, retry next pass
	wakeInFlight                        // another claimant's unexpired claim: wait, the mark stays
)

// sendWake claims and sends one wake and reports its outcome (see wakeOutcome).
func (d *WakeDispatcher) sendWake(ctx context.Context, key model.WakeKey, hop int64) (wakeOutcome, error) {
	attempt, won, state, err := d.budgets.claimState(ctx, key)
	if err != nil {
		return wakeNotAttempted, fmt.Errorf("claim wake candidate: %w", err)
	}
	switch state {
	case claimBudgetHeld:
		return wakeBudgetHeld, nil
	case claimInFlight:
		return wakeInFlight, nil
	}
	if !won {
		return wakeAttempted, nil
	}
	if attempt.Token == "" || attempt.Key != key {
		return wakeNotAttempted, fmt.Errorf("wake attempt fence is missing or mismatched: %w", model.ErrConflict)
	}
	attempt.Hop = hop
	sendErr := d.send(ctx, attempt)
	outcome := model.WakeDelivered
	if sendErr != nil {
		outcome = model.WakeError
	}
	// Once a send ran, caller cancellation must not turn its failure into a retry.
	finishCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	recordErr := d.store.RecordWakeOutcome(finishCtx, attempt, outcome)
	var failures error
	if sendErr != nil {
		failures = fmt.Errorf("send wake: %w", sendErr)
	}
	if recordErr != nil {
		failures = errors.Join(failures, fmt.Errorf("record wake completion: %w", recordErr))
	}
	return wakeAttempted, failures
}
