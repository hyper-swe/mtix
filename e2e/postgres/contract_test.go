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
// Contract tests: none are implemented, and none is a placeholder.
//
// This file used to hold ten tests (CRUD, list filters, concurrent
// mutations, concurrent schema migration, TLS verify-full, statement timeout,
// audit-log triggers, audit-log atomicity, node-type canonicalization, SQL
// injection) whose bodies only called Ping on a store the suite could not
// open: openStore skips on ErrPGStoreNotReady, and nothing registers a real
// opener, so each either skipped or passed without asserting its behavior.
// They are removed (MTIX-95.8.4). What each named is covered, or absent, as
// follows:
//
//   - Audit-log triggers: the row triggers (UPDATE, DELETE) are asserted by
//     TestAuditLog_RowTriggersRefuseUpdateAndDelete and the TRUNCATE guard by
//     TestTruncateGuard_BlocksOwnerTruncate, both in
//     internal/store/postgres/transport.
//   - Audit-log atomicity with a mutation: UNTESTED because the behavior does
//     not exist. mtix never writes audit_log
//     (TestAuditLog_PushWritesNoRows pins that), so there is no audit row to
//     commit or roll back with a mutation.
//   - Concurrent schema migration: internal/store/postgres/transport tests.
//   - TLS posture, SQL injection: transport tests (dsn_test.go,
//     security_test.go). Statement timeout: TestPool_StatementTimeoutApplied
//     (integration_test.go) proves the setting is applied; a query actually
//     aborted at the timeout is UNTESTED.
//   - CRUD, list filters, concurrent mutations and node-type
//     canonicalization: UNTESTED here because the hub has no node
//     store; nodes live in the local SQLite store, tested in
//     internal/store/sqlite.
//
// A new contract test must assert real behavior through a registered opener.
// ---------------------------------------------------------------------------
