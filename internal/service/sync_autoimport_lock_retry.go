// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

// ErrAutoImportLockBusy means sync ownership stayed busy throughout the short
// FR-15.2 import retry. The caller may read the cache but cannot claim freshness.
var ErrAutoImportLockBusy = errors.New("auto-import sync lock busy")

// Tune the bounded startup retry here; all service instances share the policy,
// while clock and sleep dependencies remain local to each instance.
const (
	autoImportLockRetryBudget   = 500 * time.Millisecond
	autoImportLockRetryInterval = 25 * time.Millisecond
)

// acquireAutoImportLock retries only native contention (FR-15.2, MTIX-149).
// Scheduled time is counted as well as elapsed time: even a frozen or backwards
// injected clock cannot cause an unbounded loop. Cancellation interrupts waits.
func (s *SyncService) acquireAutoImportLock(ctx context.Context, dir string) (*os.File, error) {
	start := s.clock()
	remaining := autoImportLockRetryBudget
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("auto-import ownership: %w", err)
		}
		lock, err := s.acquireLock(dir, lockExclusive)
		if err == nil {
			return lock, nil
		}
		if !isSyncLockContention(err) {
			return nil, fmt.Errorf("auto-import ownership: %w", err)
		}
		remaining = min(remaining, autoImportLockRetryBudget-s.clock().Sub(start))
		if remaining <= 0 {
			return nil, fmt.Errorf("auto-import ownership: %w: %w", ErrAutoImportLockBusy, err)
		}
		delay := min(remaining, autoImportLockRetryInterval)
		if err := s.retrySleep(ctx, delay); err != nil {
			return nil, fmt.Errorf("auto-import ownership retry: %w", err)
		}
		remaining -= delay
	}
}

func waitAutoImportRetry(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
