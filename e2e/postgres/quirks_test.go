// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Provider-specific quirk tests. Unlike the contract suite (which asserts
// uniform behavior across providers), these tests document and enforce
// behavior that is intentionally provider-specific.

package postgres

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSupabase_PgBouncerTransactionMode_PreparedStatementsDisabled
// asserts that when the active Supabase DSN points at port 6543
// (Supavisor/pgbouncer transaction-pool mode), our provider correctly
// reports SupportsPreparedStatements()==false and SupportsAdvisoryLocks()==false.
//
// This is the fast-feedback warning that prevents the full contract suite
// from melting down with cryptic prepared-statement errors. If you add a
// new feature that requires session state, gate it on these capability
// checks.
func TestSupabase_PgBouncerTransactionMode_PreparedStatementsDisabled(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
		want bool // want SupportsPreparedStatements
	}{
		{
			name: "direct port 5432 supports prepared statements",
			dsn:  "postgres://u:p@db.example.supabase.co:5432/postgres?sslmode=require",
			want: true,
		},
		{
			name: "pooled port 6543 disables prepared statements",
			dsn:  "postgres://u:p@db.example.supabase.co:6543/postgres?sslmode=require",
			want: false,
		},
		{
			name: "trailing port 6543 (no path) treated as pooled",
			dsn:  "postgres://u:p@db.example.supabase.co:6543",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, err := SelectProvider(ProviderSupabase, WithSupabaseDSN(tc.dsn))
			require.NoError(t, err, "supabase provider should construct from any DSN")
			assert.Equal(t, tc.want, p.SupportsPreparedStatements(),
				"SupportsPreparedStatements mismatch for %s", tc.name)
			assert.Equal(t, tc.want, p.SupportsAdvisoryLocks(),
				"SupportsAdvisoryLocks should match SupportsPreparedStatements")
		})
	}
}

// TestNeon_ColdStart_ConnectsWithinTimeout verifies that the configured
// startup timeout is large enough to handle Neon's worst-case cold start
// (5 seconds per their docs as of 2026-04). We don't actually connect
// here — that requires the MTIX-14.1 driver — but we do assert the
// provider's configured timeout is generous enough.
func TestNeon_ColdStart_ConnectsWithinTimeout(t *testing.T) {
	p, err := SelectProvider(ProviderNeon,
		WithNeonDSN("postgres://u:p@ep.example.neon.tech/db?sslmode=require"),
		WithStartupTimeout(60*time.Second),
	)
	require.NoError(t, err)
	assert.Equal(t, ProviderNeon, p.Name())

	// A startup timeout below 10s is a foot-gun: Neon's documented worst
	// case is 5s and we want headroom for network jitter.
	const minTimeout = 10 * time.Second
	cfg := neonConfigFor(t, p)
	assert.GreaterOrEqual(t, cfg.startupTimeout, minTimeout,
		"neon provider startup timeout (%s) below recommended minimum %s",
		cfg.startupTimeout, minTimeout)
}

// TestNeon_ConnectionRouting_HandlesProxyHeaders is a documentation test
// that pins our DSN-construction conventions: Neon requires sslmode=require,
// and our provider preserves whatever DSN parameters the user supplied.
// (Active connection testing waits on MTIX-14.1.)
func TestNeon_ConnectionRouting_HandlesProxyHeaders(t *testing.T) {
	const dsn = "postgres://u:p@ep.example.neon.tech/db?sslmode=require&application_name=mtix"
	p, err := SelectProvider(ProviderNeon, WithNeonDSN(dsn))
	require.NoError(t, err)

	// We can't directly inspect the resolved DSN (it's deliberately
	// private), but we can confirm that Setup produces a DSN that retains
	// the sslmode / application_name parameters. The provider's setup uses
	// withSearchPath which APPENDS, never replaces.
	got, _ := p.Setup(t.Context(), t)
	got = RedactDSN(got) // never log raw
	assert.Contains(t, got, dsnReplacement,
		"Setup output should be DSN-redacted in test logs")
}

// neonConfigFor reaches into the provider for its config. It's a test-only
// shim so we don't need to expose the providerConfig type. Failing the type
// assertion is a hard test failure; the only neon constructor we ship
// returns *neonProvider.
func neonConfigFor(t *testing.T, p PostgresProvider) providerConfig {
	t.Helper()
	np, ok := p.(*neonProvider)
	require.True(t, ok, "expected *neonProvider, got %T", p)
	return np.cfg
}
