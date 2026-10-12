// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Completion publication follows the entire owned pull pipeline.
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func exchangeService(st *sqlite.Store) *service.SyncService {
	if app.syncSvc != nil && app.store == st {
		return app.syncSvc
	}
	return service.NewSyncService(st, slog.Default(), time.Now)
}

func completePullExchange(ctx context.Context, owner *service.ExchangePull, tracker *pullExchangeTracker, res pullOutcome) error {
	return owner.Complete(ctx, service.ExchangePullCompletion{
		CursorDrained: tracker.drained, SweepCompleted: res.sweep.Completed,
		Encountered: tracker.encountered(), Reason: tracker.reason,
	})
}
