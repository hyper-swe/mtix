// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

package postgres

import (
	"context"
	"strings"
	"testing"
)

// supabaseProvider runs the contract suite against a managed Supabase
// project. It does NOT provision the project; the project must be supplied
// via MTIX_TEST_SUPABASE_DSN (or WithSupabaseDSN in tests).
//
// Isolation strategy: each test gets a unique schema (mtix_test_<ts>_<rand>)
// and the cleanup hook drops the schema CASCADE. We do not create a fresh
// database because Supabase free tier doesn't permit CREATE DATABASE.
//
// Quirks worth knowing (see quirks_test.go and README.md):
//   - Port 6543 (Supavisor / pgbouncer transaction mode) breaks prepared
//     statements and session-level advisory locks. Tests that need either
//     should consult SupportsPreparedStatements / SupportsAdvisoryLocks.
//   - Port 5432 ("direct" mode) supports both, but enforces a stricter
//     concurrent-connection cap.
type supabaseProvider struct {
	cfg providerConfig
	dsn string
}

func newSupabaseProvider(cfg providerConfig) (*supabaseProvider, error) {
	dsn, err := resolveDSN(cfg.supabaseDSN, EnvSupabaseDSN)
	if err != nil {
		return nil, err
	}
	return &supabaseProvider{cfg: cfg, dsn: dsn}, nil
}

func (p *supabaseProvider) Name() string { return ProviderSupabase }

// SupportsAdvisoryLocks returns true only when the DSN points at the direct
// connection port (5432). Transaction-mode pooling (6543) rotates server
// sessions per statement, so session-scoped advisory locks are unreliable.
func (p *supabaseProvider) SupportsAdvisoryLocks() bool {
	return !isPgBouncerTxnMode(p.dsn)
}

// SupportsPreparedStatements has the same DSN-based logic as
// SupportsAdvisoryLocks for the same multiplexing reason.
func (p *supabaseProvider) SupportsPreparedStatements() bool {
	return !isPgBouncerTxnMode(p.dsn)
}

// Setup returns a per-test DSN that targets a freshly-created schema.
// The schema is dropped on cleanup. The driver is responsible for setting
// search_path; we encode it into the DSN as a Postgres `options` query
// parameter so the store driver does not need provider-specific knowledge.
func (p *supabaseProvider) Setup(_ context.Context, t *testing.T) (string, func()) {
	t.Helper()

	// MTIX-14.1 (the actual driver) is what knows how to create/drop the
	// schema; until that lands, tests that call Setup will t.Skip via the
	// shared contract suite when the store reports "not implemented".
	// We still construct the unique-name DSN so the harness path is
	// exercised end-to-end.
	schema := uniqueSchemaName(p.cfg.suiteTag)
	dsn := withSearchPath(p.dsn, schema)

	cleanup := func() {
		// Cleanup is a no-op until 14.1 provides a SQL handle. The nuclear
		// cleanup script (tools/cleanup-test-schemas.go) handles orphans.
	}
	t.Cleanup(cleanup)
	return dsn, cleanup
}

// isPgBouncerTxnMode is a coarse heuristic: Supabase routes pooled traffic
// through port 6543, direct traffic through 5432. We accept that this can
// produce false positives for self-hosted setups; the function is purely
// advisory (drives SupportsAdvisoryLocks) and never gates correctness.
func isPgBouncerTxnMode(dsn string) bool {
	// Match :6543/ or :6543? to avoid mistaking arbitrary digits.
	return strings.Contains(dsn, ":6543/") ||
		strings.Contains(dsn, ":6543?") ||
		strings.HasSuffix(dsn, ":6543")
}

// withSearchPath appends an `options=-c%20search_path%3D<schema>` query
// parameter to the DSN, which Postgres interprets as a per-connection
// `SET search_path = <schema>`. Existing query params are preserved.
//
// The encoding uses URL-escaped space (%20) and equals (%3D) because the
// `options` value is parsed by libpq as a shell-style string.
func withSearchPath(dsn, schema string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}
	return dsn + sep + "options=-c%20search_path%3D" + schema
}
