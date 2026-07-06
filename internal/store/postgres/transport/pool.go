// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool wraps a pgxpool.Pool with mtix-specific defaults applied at
// open time per FR-18 / SYNC-DESIGN section 5. Use New to construct.
type Pool struct {
	p *pgxpool.Pool

	// clientMachineHash and clientCLIVersion identify the calling CLI
	// for the version-negotiation gate (ADR-003 §7 Phase 1.5/3). They
	// are empty until SetClientIdentity is called; while empty, push
	// does not record a client row (see UpsertProjectClient).
	clientMachineHash string
	clientCLIVersion  string
}

// SetClientIdentity records the calling CLI's machine hash and build
// version so subsequent pushes upsert a sync_project_clients row for the
// version gate (ADR-003 §7 Phase 1.5/3). Callers that omit this (e.g. a
// read-only tool) simply never register a client; the gate stays closed
// for that project, which is the safe default.
//
// Not safe for concurrent use with PushEvents on the same Pool; set the
// identity once right after New, before any push. Empty strings are
// treated as "no identity".
func (p *Pool) SetClientIdentity(machineHash, cliVersion string) {
	if p == nil {
		return
	}
	p.clientMachineHash = machineHash
	p.clientCLIVersion = cliVersion
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
//
// MaxConns=5 is the FR-18 / MTIX-15.10 ceiling: at 10 active
// developers per hub × 5 conns/CLI = 50 connections, well within
// the 100-conn limits of common managed PG providers. Push and pull
// each use one connection at a time; the headroom covers the
// daemon's health check + a transient overlap.
func DefaultPoolDefaults() PoolDefaults {
	return PoolDefaults{
		MaxConns:          5,
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
		// Apply statement_timeout via SET after connect, NOT as a startup
		// RuntimeParam. Managed Postgres proxies/poolers (Neon, Supabase)
		// silently drop unknown startup parameters, so the RuntimeParam
		// no-ops and the query cap is never enforced on cloud (Neon returns
		// "0", Supabase its own default). A SET is ordinary SQL the proxy
		// passes through — verified against Neon (direct + pooler) and the
		// Supabase session pooler. Runs on every new pooled connection. The
		// value is an integer we control, so the formatted SQL carries no
		// injection surface.
		stmtTimeoutMS := defs.StatementTimeout.Milliseconds()
		cfg.AfterConnect = func(ctx context.Context, conn *pgx.Conn) error {
			if _, execErr := conn.Exec(ctx, fmt.Sprintf("SET statement_timeout = %d", stmtTimeoutMS)); execErr != nil {
				return fmt.Errorf("apply statement_timeout: %w", execErr)
			}
			return nil
		}
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, hintTLSTrust(enforced, fmt.Errorf("pgxpool open: %w", err))
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, hintTLSTrust(enforced, fmt.Errorf("initial ping: %w", err))
	}
	return &Pool{p: pool}, nil
}

// hintTLSTrust augments a connection error with actionable guidance when it is
// a TLS certificate-verification failure and no CA was supplied. Managed
// Postgres providers (notably Supabase) serve certificates that chain to a
// PRIVATE CA absent from the system trust store; Go's raw x509 error
// ("certificate is not standards compliant" / "failed to verify certificate")
// gives the operator no clue that the fix is a one-line sslrootcert. Non-cert
// errors, and cases where a CA was already supplied, pass through unchanged.
func hintTLSTrust(enforcedDSN string, err error) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	certFailure := strings.Contains(msg, "failed to verify certificate") ||
		strings.Contains(msg, "x509:") ||
		strings.Contains(msg, "unknown authority")
	if !certFailure {
		return err
	}
	// A CA was already supplied — this is a different TLS problem; don't mislead.
	if strings.Contains(enforcedDSN, "sslrootcert=") || os.Getenv(EnvSSLRootCert) != "" {
		return err
	}
	return fmt.Errorf("%w\n\nhint: TLS verification failed because the server's "+
		"certificate chains to a private CA not in the system trust store "+
		"(common with Supabase and some managed Postgres). Download your "+
		"provider's CA certificate and add sslrootcert=<path> to the DSN, or "+
		"export %s=<path>, then retry", err, EnvSSLRootCert)
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
