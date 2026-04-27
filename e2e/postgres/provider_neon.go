// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package postgres

import (
	"context"
	"testing"
	"time"
)

// neonProvider runs the contract suite against Neon serverless Postgres.
// DSN must be supplied via MTIX_TEST_NEON_DSN (or WithNeonDSN in tests).
//
// Isolation strategy: unique schema per test (same approach as Supabase).
// A future enhancement may use Neon's branching API to give each test a
// fully isolated branch, but that requires Neon API key handling that we
// defer to MTIX-14.9.1.
//
// Quirks worth knowing (see quirks_test.go and README.md):
//   - Cold starts: a Neon compute instance idle for >5 min spins down,
//     and the next connection takes 2–5 seconds. The default startup
//     timeout (60s) is generous to handle this; tests should NOT tighten
//     it without consulting this comment.
//   - Connection routing: Neon uses an HTTP-like proxy that injects
//     `endpoint=` SNI headers. Standard pgx/lib-pq drivers handle this
//     transparently as long as the DSN includes `?sslmode=require`.
type neonProvider struct {
	cfg providerConfig
	dsn string
}

func newNeonProvider(cfg providerConfig) (*neonProvider, error) {
	dsn, err := resolveDSN(cfg.neonDSN, EnvNeonDSN)
	if err != nil {
		return nil, err
	}
	// Neon ALWAYS supports prepared statements and advisory locks on the
	// "compute endpoint" connection; the proxy is HTTP-only at the edge,
	// not a connection pooler.
	return &neonProvider{cfg: cfg, dsn: dsn}, nil
}

func (p *neonProvider) Name() string                     { return ProviderNeon }
func (p *neonProvider) SupportsAdvisoryLocks() bool      { return true }
func (p *neonProvider) SupportsPreparedStatements() bool { return true }

// Setup returns a per-test DSN. As with Supabase, schema CREATE/DROP is
// the responsibility of the driver under test (MTIX-14.1) — until that
// lands, the contract suite will t.Skip on a "not implemented" error.
//
// We deliberately do NOT shorten the configured startup timeout: the
// driver's own connect path must tolerate 2–5s cold starts.
func (p *neonProvider) Setup(_ context.Context, t *testing.T) (string, func()) {
	t.Helper()

	// Surface the configured timeout in test logs so flakiness is debuggable.
	// (No DSN is logged — only the timeout, which is not a secret.)
	if p.cfg.startupTimeout < 5*time.Second {
		t.Logf("warning: startup timeout %s may be too short for Neon cold start",
			p.cfg.startupTimeout)
	}

	schema := uniqueSchemaName(p.cfg.suiteTag)
	dsn := withSearchPath(p.dsn, schema)

	cleanup := func() {
		// As with supabase, no-op until MTIX-14.1 provides a connection.
	}
	t.Cleanup(cleanup)
	return dsn, cleanup
}
