// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
)

// readPool is the one choke point for reads on the reader connection pool
// (MTIX-250). It holds the *sql.DB privately and exposes only the read
// methods, so no code in the store can query the pool except through it. The
// pool's connections come from shieldConnector (openShieldedSQLite), which
// keeps a read cancelled mid-query from leaving a stale WAL snapshot on a
// pooled connection (see read_shield.go). A source-scan test
// (read_pool_guard_test.go) fails if a new read bypasses this type.
type readPool struct {
	rawDB *sql.DB // read only inside this file; the guard test fails on any other use
}

// ReadPool is the exported name of readPool, the type Store.ReadDB returns so
// callers outside this package can read through the choke point.
type ReadPool = readPool

// newReadPool wraps a pool opened by openShieldedSQLite.
func newReadPool(db *sql.DB) *readPool { return &readPool{rawDB: db} }

// QueryContext runs a query on the reader pool.
func (p *readPool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.rawDB.QueryContext(ctx, query, args...) //nolint:wrapcheck // thin pool method; callers wrap with their own context
}

// QueryRowContext runs a single-row query on the reader pool.
func (p *readPool) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	return p.rawDB.QueryRowContext(ctx, query, args...)
}

// BeginTx starts a read transaction on the reader pool.
func (p *readPool) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.rawDB.BeginTx(ctx, opts) //nolint:wrapcheck // thin pool method; callers wrap with their own context
}

// reader returns the pool for callers outside the store (Store.ReadDB).
func (p *readPool) reader() *ReadPool { return p }

// shares reports whether the pool is the given handle (a read-only store uses
// one connection for both roles and must close it once).
func (p *readPool) shares(db *sql.DB) bool { return p.rawDB == db }

// Query runs a query on the reader pool with no cancellation (sql.DB parity).
func (p *readPool) Query(query string, args ...any) (*sql.Rows, error) {
	return p.QueryContext(context.Background(), query, args...)
}

// QueryRow runs a single-row query on the reader pool with no cancellation
// (sql.DB parity).
func (p *readPool) QueryRow(query string, args ...any) *sql.Row {
	return p.QueryRowContext(context.Background(), query, args...)
}

// ExecContext runs a statement on one reader connection. Only a session-local
// statement belongs here (a TEMP object a test builds on a one-connection
// pool); the store never writes through the reader pool.
func (p *readPool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.rawDB.ExecContext(ctx, query, args...) //nolint:wrapcheck // thin pool method
}

// Conn returns one reader connection. The connection comes from the shielded
// pool, so a query or prepared statement on it is shielded like a pool query
// (read_shield.go); the guard test forbids preparing on it outside the shield.
func (p *readPool) Conn(ctx context.Context) (*sql.Conn, error) {
	return p.rawDB.Conn(ctx) //nolint:wrapcheck // thin pool method
}

// Stats returns the pool statistics.
func (p *readPool) Stats() sql.DBStats { return p.rawDB.Stats() }

// SetMaxOpenConns sets the pool size; tests use 1 so the next read reuses the
// connection a cancelled read ran on.
func (p *readPool) SetMaxOpenConns(n int) { p.rawDB.SetMaxOpenConns(n) }

// SetMaxIdleConns sets the idle pool size.
func (p *readPool) SetMaxIdleConns(n int) { p.rawDB.SetMaxIdleConns(n) }

// Close closes the pool.
func (p *readPool) Close() error { return p.rawDB.Close() } //nolint:wrapcheck // thin pool method; Store.Close wraps it
