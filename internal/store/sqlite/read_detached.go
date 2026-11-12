// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"time"
)

// readQueryBound caps one detached read; far above any inbox read, it only
// stops a read stuck on a lock from running unbounded.
const readQueryBound = 30 * time.Second

// detachedRead returns a context for one short reader-pool query that the
// caller's cancellation cannot reach once the query has started (MTIX-112.3).
//
// Driver behavior: in modernc.org/sqlite v1.35.0, stmt.query (sqlite.go, the
// deferred cleanup) returns ctx.Err() and discards the already-stepped rows
// without closing them when the context is cancelled after the first step. The
// prepared statement is then never finalized, so the pooled reader connection
// keeps an open WAL read snapshot and later reads on it miss newer commits (a
// cancelled wait still showed as parked).
//
// So the read runs uncancelled: the query completes and its rows are closed
// normally. It is bounded by readQueryBound (30s) so a read stuck on a lock
// cannot run forever. An already-cancelled context is refused up front, before
// any statement is prepared. release must be called when the read and its rows
// are finished.
//
// Scope: only the inbox wait-path reads use this (InboxCount, InboxParked,
// inboxList). The other cancellable reader-pool reads share the driver
// behavior and are tracked by MTIX-250; do not widen this change to them.
func detachedRead(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, fmt.Errorf("read cancelled before start: %w", err)
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), readQueryBound)
	return dctx, cancel, nil
}
