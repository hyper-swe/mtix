// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// pushQuerier is what the push reads need: the store's read pool, or the
// write transaction of a PushLocked.
type pushQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// readPool adapts the store's read pool to pushQuerier.
type readPool struct{ s *Store }

// QueryContext runs query on the read pool.
func (r readPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return r.s.Query(ctx, query, args...)
}

// PushLocked is the push's view of the board while it holds the write lock
// (MTIX-95.39): every read and every hold it records runs in one write
// transaction (BEGIN IMMEDIATE), so no other connection can commit between
// them. Push uses it for the last attempt of a batch, which then needs no
// check that the store stayed put.
type PushLocked struct {
	s  *Store
	tx *sql.Tx
}

// WithPushLock runs fn in one write transaction and gives it the PushLocked
// view. A nil error from fn commits it; an error rolls it back.
func (s *Store) WithPushLock(ctx context.Context, fn func(PushLocked) error) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error { return fn(PushLocked{s: s, tx: tx}) })
}

// Now returns the store's clock reading in UTC, as Store.Now does.
func (l PushLocked) Now() time.Time { return l.s.Now() }

// HeldPushCreations is Store.HeldPushCreations inside the lock.
func (l PushLocked) HeldPushCreations(ctx context.Context) ([]HeldPushEvent, error) {
	return heldPushCreations(ctx, l.tx)
}

// PushSubjects is Store.PushSubjects inside the lock.
func (l PushLocked) PushSubjects(ctx context.Context, eventIDs []string) (map[string]PushSubject, error) {
	return pushSubjects(ctx, l.tx, eventIDs)
}

// CountHeldPushEvents is Store.CountHeldPushEvents inside the lock.
func (l PushLocked) CountHeldPushEvents(ctx context.Context) (int, error) {
	var n int
	// Every held push event.
	if err := l.tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM sync_quarantine WHERE source = 'push'`).Scan(&n); err != nil {
		return 0, fmt.Errorf("count held push events: %w", err)
	}
	return n, nil
}

// HoldPushEvents is Store.HoldPushEvents inside the lock.
func (l PushLocked) HoldPushEvents(ctx context.Context, holds []QuarantinedEvent, cliVersion string) error {
	if len(holds) == 0 {
		return nil
	}
	return holdPushEvents(ctx, l.tx, l.s.Now(), holds, cliVersion)
}

// DataVersion is Store.DataVersion read on the lock's connection, so push
// can note the version its index now stands at.
func (l PushLocked) DataVersion(ctx context.Context) (int64, error) {
	var v int64
	// The write connection's view of commits made by other connections.
	if err := l.tx.QueryRowContext(ctx, `PRAGMA data_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read data version: %w", err)
	}
	return v, nil
}
