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

func (d *WakeDispatcher) sendWake(ctx context.Context, key model.WakeKey, hop int64) error {
	attempt, won, err := d.store.ClaimWake(ctx, key, time.Minute)
	if err != nil {
		return fmt.Errorf("claim wake candidate: %w", err)
	}
	if !won {
		return nil
	}
	if attempt.Token == "" || attempt.Key != key {
		return fmt.Errorf("wake attempt fence is missing or mismatched: %w", model.ErrConflict)
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
	return failures
}
