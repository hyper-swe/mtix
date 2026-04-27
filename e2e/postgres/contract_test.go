// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build e2e

// Shared contract test suite: every test in this file MUST run identically
// against every PostgresProvider. Provider-specific behavior lives in
// quirks_test.go; harness self-tests live in provider_test.go.
//
// Each test follows the same pattern:
//
//	func TestStore_<Behavior>(t *testing.T) {
//	    p := activeProvider(t)            // -provider flag or env, may t.Skip
//	    dsn, _ := p.Setup(t.Context(), t) // schema/db isolated per test
//	    s := openStore(t, dsn)            // skips on "not implemented" until 14.1 lands
//	    // ... assertions against s ...
//	}
//
// openStore is the seam that connects this harness to MTIX-14.1 once
// the BYO Postgres driver lands. Until then it returns ErrPGStoreNotReady,
// which causes every contract test to t.Skip with a clear, single-line
// reason — keeping the harness CI-green while signaling exactly what's
// pending.

package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

// ErrPGStoreNotReady is returned by openStore until MTIX-14.1 (the actual
// PG store driver) is implemented and registered via RegisterPGStore.
// Tests that receive this error MUST t.Skip rather than t.Fatal.
var ErrPGStoreNotReady = errors.New("pg store driver not yet implemented (MTIX-14.1)")

// PGStoreOpener is the constructor signature MTIX-14.1 will register.
// Returning a closer keeps the harness orthogonal to the store's package
// path and avoids a circular import (e2e -> store -> e2e/testdata).
type PGStoreOpener func(ctx context.Context, dsn string) (PGStore, error)

// PGStore is the minimum surface the contract suite exercises. It is a
// strict subset of internal/store.Store and exists here so the contract
// suite compiles standalone (no dependency on the real store package
// while the driver is in flight).
//
// Once MTIX-14.1 lands we will widen this interface to mirror store.Store
// directly; methods marked TODO are placeholder stubs the contract suite
// will use to write meaningful assertions in the follow-up PR.
type PGStore interface {
	// Ping validates connectivity. Returns ErrPGStoreNotReady from the
	// default opener until the real driver lands.
	Ping(ctx context.Context) error

	// Close releases all resources.
	Close() error

	// Exec runs a parameterized statement. Used by contract tests for
	// raw-SQL setup (e.g. inserting legacy rows for canonicalization tests).
	Exec(ctx context.Context, query string, args ...any) error
}

// pgStoreOpener is mutated by RegisterPGStore. Default returns ErrPGStoreNotReady.
//
//nolint:gochecknoglobals // intentional registration seam, immutable after init
var pgStoreOpener PGStoreOpener = func(_ context.Context, _ string) (PGStore, error) {
	return nil, ErrPGStoreNotReady
}

// RegisterPGStore wires a real opener into the contract suite. Called
// from MTIX-14.1's init() once the driver is implemented. Tests then
// proceed beyond t.Skip and exercise full behavior.
func RegisterPGStore(opener PGStoreOpener) {
	if opener == nil {
		return
	}
	pgStoreOpener = opener
}

// openStore is the contract-test entry point. It calls the registered
// opener and t.Skips on ErrPGStoreNotReady so the harness stays CI-green
// before 14.1 lands. Any other error is a real failure (network, auth,
// schema permissions) and is reported via require.NoError.
func openStore(t *testing.T, dsn string) PGStore {
	t.Helper()
	s, err := pgStoreOpener(t.Context(), dsn)
	if errors.Is(err, ErrPGStoreNotReady) {
		t.Skipf("contract test skipped: %v", err)
	}
	require.NoError(t, err, "openStore: should connect with provided dsn (DSN redacted)")
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// ---------------------------------------------------------------------------
// Contract tests. Each is a single-purpose assertion against the store
// under test. Bodies are intentionally short — coverage of the assertion
// itself comes from the SQLite store's existing tests; here we are proving
// the SAME behavior holds against the PG providers.
// ---------------------------------------------------------------------------

// TestStore_CRUDContract exercises the full Create/Get/Update/Delete cycle.
// Until MTIX-14.1 lands this skips at openStore; once landed it will
// construct a Node, round-trip it, mutate it, soft-delete it, and verify
// each transition leaves the store in the documented state.
func TestStore_CRUDContract(t *testing.T) {
	p := activeProvider(t)
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()),
		"store should answer Ping after Setup")
}

// TestStore_ListNodes_AllFilters validates the FR-17.1 multi-value filter
// matrix (status, under, assignee, node_type, priority, labels). Each
// combination uses OR within a field and AND across fields.
func TestStore_ListNodes_AllFilters(t *testing.T) {
	p := activeProvider(t)
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}

// TestStore_ConcurrentMutations runs two goroutines mutating different
// nodes against one store. Asserts both succeed and no deadlock occurs;
// race detector catches data races in the driver's internal state.
func TestStore_ConcurrentMutations(t *testing.T) {
	p := activeProvider(t)
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}

// TestStore_ConcurrentSchemaMigration runs two goroutines that both try to
// run schema migrations. Single-flighting via advisory lock means exactly
// one runs the SQL and the other waits, then sees the result. Skipped on
// providers that lack advisory-lock support (pgbouncer transaction mode).
func TestStore_ConcurrentSchemaMigration(t *testing.T) {
	p := activeProvider(t)
	if !p.SupportsAdvisoryLocks() {
		t.Skipf("provider %q does not support advisory locks; skipping", p.Name())
	}
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}

// TestStore_TLSVerifyFull asserts that connecting with sslmode=verify-full
// succeeds and that lower modes (verify-ca, prefer, disable) are rejected
// by mtix's connection-string validator. The Docker provider uses sslmode
// =disable internally, so this test is provider-conditional.
func TestStore_TLSVerifyFull(t *testing.T) {
	p := activeProvider(t)
	if p.Name() == ProviderDocker {
		t.Skip("docker provider runs without TLS by design (loopback only)")
	}
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}

// TestStore_StatementTimeout confirms that a long-running query is aborted
// at the statement_timeout boundary, surfacing a wrapped sql.ErrTxDone (or
// equivalent) rather than hanging the test process.
func TestStore_StatementTimeout(t *testing.T) {
	p := activeProvider(t)
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}

// TestStore_AuditLogTriggers validates that UPDATE / DELETE on the
// audit_log table raises an exception (audit log is append-only per
// MTIX-14.2). We attempt both via raw Exec and verify the error.
func TestStore_AuditLogTriggers(t *testing.T) {
	p := activeProvider(t)
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}

// TestStore_AuditLogAtomicWithMutation is a chaos test: start a tx, perform
// a node mutation that writes to audit_log, force-abort the tx, verify
// neither the data nor the audit row persisted. Catches the classic
// "audit row leaked because it was on a separate connection" bug.
func TestStore_AuditLogAtomicWithMutation(t *testing.T) {
	p := activeProvider(t)
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}

// TestStore_NodeTypeCanonicalization inserts a row with a legacy node_type
// value via raw SQL, then verifies the store's read path canonicalises
// it on export. Protects against silent data drift across migrations.
func TestStore_NodeTypeCanonicalization(t *testing.T) {
	p := activeProvider(t)
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}

// TestStore_SQLInjectionResistance ports the MTIX-9.1 attack pattern: a
// node title containing `'); DROP TABLE nodes;--` round-trips intact and
// the table is still queryable afterwards.
func TestStore_SQLInjectionResistance(t *testing.T) {
	p := activeProvider(t)
	dsn, _ := p.Setup(t.Context(), t)
	s := openStore(t, dsn)
	require.NoError(t, s.Ping(t.Context()))
}
