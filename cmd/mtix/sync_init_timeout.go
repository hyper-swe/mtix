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
		return fmt.Errorf("hub migration exceeded --migrate-timeout=%s (including lock wait); retry as the hub owner with mtix sync init --migrate-timeout=15m, then run mtix sync doctor: %w", budget, context.DeadlineExceeded)
	}
	return err
}
