// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// detachedRead returns a context for one reader-pool query that the caller's
// cancellation cannot reach once the query has started (MTIX-112.3, MTIX-250).
//
// Driver behavior: in modernc.org/sqlite v1.35.0, stmt.query (sqlite.go, the
// deferred cleanup) returns ctx.Err() and discards the already-stepped rows
// without closing them when the context is cancelled after the first step. The
// prepared statement is then never finalized, so the pooled reader connection
// keeps an open WAL read snapshot and later reads on it miss newer commits (a
// cancelled wait still showed as parked).
//
// So the first step always runs to completion: the query finishes and its rows
// are closed normally, and a cancel takes effect between rows (shieldRows). No
// time bound is set, because a bound would interrupt a long first step
// (integrity_check on a large board) with the same driver path; a read stuck on
// a lock is bounded by PRAGMA busy_timeout. An already-cancelled context is
// refused up front, before any statement is prepared. release must be called
// when the query call returns; it is a no-op kept so callers need not change.
//
// Scope: every reader-pool query runs through this, because the pool's shielded
// connections call it (read_shield.go, MTIX-250); it covers all reader-pool
// reads, not only the inbox reads of MTIX-112.3.
func detachedRead(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, func() {}, fmt.Errorf("read cancelled before start: %w", err)
	}
	return context.WithoutCancel(ctx), func() {}, nil
}
