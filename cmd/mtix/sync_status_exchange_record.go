// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Status exposes receipt presence separately from recorded omission reasons.
package main

import (
	"context"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func readExchangeStatus(ctx context.Context, st *sqlite.Store) (service.ExchangeRecordStatus, error) {
	return exchangeService(st).ExchangeRecordStatus(ctx, app.mtixDir)
}
