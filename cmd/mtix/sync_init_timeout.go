// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

const defaultSyncMigrateTimeout = 5 * time.Minute

type syncInitOptions struct {
	transport      transport.Options
	migrateTimeout time.Duration
}

// migrateSyncInit bounds migration separately from connecting (FR-18.14).
// The parent still propagates caller cancellation, including CLI interrupts.
func migrateSyncInit(ctx context.Context, budget time.Duration, migrate func(context.Context) error) error {
	migrateCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	err := migrate(migrateCtx)
	if err != nil && ctx.Err() == nil && migrateCtx.Err() == context.DeadlineExceeded {
		return syncInitMigrationTimeoutError(budget, err)
	}
	return err
}

// syncInitMigrationTimeoutError keeps both deadline and server context (FR-18.14).
func syncInitMigrationTimeoutError(budget time.Duration, cause error) error {
	retry, larger := syncInitMigrationRetryBudget(budget)
	if !larger {
		return fmt.Errorf("hub migration exceeded --migrate-timeout=%s (including lock wait); maximum supported duration reached; inspect hub lock holders and server work before retrying, then run mtix sync doctor: %w: %w", budget, context.DeadlineExceeded, cause)
	}
	return fmt.Errorf("hub migration exceeded --migrate-timeout=%s (including lock wait); retry as the hub owner with mtix sync init --migrate-timeout=%s, then run mtix sync doctor: %w: %w", budget, retry, context.DeadlineExceeded, cause)
}

// syncInitMigrationRetryBudget grows the failed budget safely (FR-18.14).
// Normal budgets double, with at least 15m and at most 1h automatic growth.
// At the duration limit there is no representable larger recommendation.
func syncInitMigrationRetryBudget(budget time.Duration) (time.Duration, bool) {
	const maximum = time.Duration(1<<63 - 1)
	if budget < 15*time.Minute/2 {
		return 15 * time.Minute, true
	}
	if budget == maximum {
		return 0, false
	}
	return budget + min(budget, time.Hour, maximum-budget), true
}
