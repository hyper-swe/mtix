// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool wraps a pgxpool.Pool with mtix-specific defaults applied at
// open time per FR-18 / SYNC-DESIGN section 5. Use New to construct.
type Pool struct {
	p *pgxpool.Pool
}

// PoolDefaults are the mtix-tested values for connection pooling per
// SYNC-DESIGN section 6.2 ("at most 10 active developers per hub").
// Changing these is a Sev-2 design decision — coordinate via a new
// MTIX ticket.
type PoolDefaults struct {
	MaxConns          int32
	ConnLifetime      time.Duration
	StatementTimeout  time.Duration
	HealthCheckPeriod time.Duration
}

// DefaultPoolDefaults is the canonical configuration callers MUST pass
// unless an integration test deliberately diverges (see the TLS-only
// integration tests that opt out of statement_timeout).
func DefaultPoolDefaults() PoolDefaults {
	return PoolDefaults{
		MaxConns:          8,
		ConnLifetime:      30 * time.Minute,
		StatementTimeout:  10 * time.Second,
		HealthCheckPeriod: 1 * time.Minute,
	}
}

// New parses dsn, enforces TLS posture (defaulting to verify-full),
// applies pool defaults, and opens the connection pool. Returns the
// wrapper; close it with Pool.Close.
//
// dsn flow:
//  1. Source() resolves the raw DSN from the env or .mtix/secrets.
//  2. EnforceTLSPosture() defaults sslmode and honors SSLROOTCERT.
//  3. pgxpool.ParseConfig + apply mtix defaults.
//  4. pgxpool.NewWithConfig opens the pool and runs an initial healthcheck.
//
// On any error returned from this function, the caller MUST log it
// only after passing through RedactDSN (MTIX-15.3.4) — the wrapped
// error may include a partial DSN.
func New(ctx context.Context, dsn string, opts Options) (*Pool, error) {
	return NewWithDefaults(ctx, dsn, opts, DefaultPoolDefaults())
}

// NewWithDefaults is the test seam — pass PoolDefaults to override the
// production defaults (e.g., to disable statement_timeout for a test
// that intentionally runs a long query).
func NewWithDefaults(ctx context.Context, dsn string, opts Options, defs PoolDefaults) (*Pool, error) {
	enforced, err := EnforceTLSPosture(dsn, opts)
	if err != nil {
		return nil, fmt.Errorf("tls posture: %w", err)
	}

	cfg, err := pgxpool.ParseConfig(enforced)
	if err != nil {
		return nil, fmt.Errorf("pgxpool parse: %w", err)
	}
	cfg.MaxConns = defs.MaxConns
	cfg.MaxConnLifetime = defs.ConnLifetime
	cfg.HealthCheckPeriod = defs.HealthCheckPeriod
	if defs.StatementTimeout > 0 {
		if cfg.ConnConfig.RuntimeParams == nil {
			cfg.ConnConfig.RuntimeParams = map[string]string{}
		}
		cfg.ConnConfig.RuntimeParams["statement_timeout"] =
			fmt.Sprintf("%d", defs.StatementTimeout.Milliseconds())
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("initial ping: %w", err)
	}
	return &Pool{p: pool}, nil
}

// HealthCheck pings the underlying pool. Returns nil on success.
func (p *Pool) HealthCheck(ctx context.Context) error {
	if p == nil || p.p == nil {
		return fmt.Errorf("pool not open")
	}
	return p.p.Ping(ctx)
}

// Close releases the underlying pool. Safe to call multiple times.
func (p *Pool) Close() {
	if p == nil || p.p == nil {
		return
	}
	p.p.Close()
	p.p = nil
}

// Inner returns the underlying *pgxpool.Pool so 15.3.3's PushEvents
// and 15.3.1's Migrate can run queries. Exported so the migrate.go
// runner in this package can access it without an internal getter,
// while keeping callers outside this package on the wrapper API.
func (p *Pool) Inner() *pgxpool.Pool {
	if p == nil {
		return nil
	}
	return p.p
}
