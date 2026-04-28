// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// envTestDSN is the env var that enables PG-bound tests in this file.
// Skip if unset — local laptops without PG and CI without a service
// container must still pass.
const envTestDSN = "MTIX_PG_TEST_DSN"

// requireTestDSN returns the test DSN or skips the calling test if
// the env var is unset.
func requireTestDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(envTestDSN)
	if dsn == "" {
		t.Skipf("set %s to enable transport integration tests", envTestDSN)
	}
	return dsn
}

// freshSchema drops the mtix-owned tables on the test DSN so each
// test starts clean. Safe to run on a dedicated test database;
// destructive on production — the test DSN MUST point at a throwaway DB.
func freshSchema(t *testing.T, dsn string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()

	for _, stmt := range []string{
		`DROP TABLE IF EXISTS sync_conflicts CASCADE`,
		`DROP TABLE IF EXISTS applied_events CASCADE`,
		`DROP TABLE IF EXISTS sync_events CASCADE`,
		`DROP TABLE IF EXISTS sync_projects CASCADE`,
		`DROP TABLE IF EXISTS audit_log CASCADE`,
		`DROP FUNCTION IF EXISTS audit_log_immutable() CASCADE`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoErrorf(t, err, "drop: %s", stmt)
	}
}

// openTestPool opens a transport.Pool against the test DSN and
// registers cleanup. Skips the test if no DSN is configured.
func openTestPool(t *testing.T) *transport.Pool {
	t.Helper()
	dsn := requireTestDSN(t)
	freshSchema(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// --- Pool basics ---

func TestPool_OpenAndClose(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.HealthCheck(context.Background()))
}

func TestPool_DoubleCloseSafe(t *testing.T) {
	dsn := requireTestDSN(t)
	freshSchema(t, dsn)
	pool, err := transport.New(context.Background(), dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	pool.Close()
	pool.Close() // must not panic
}

func TestPool_StatementTimeoutApplied(t *testing.T) {
	pool := openTestPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var raw string
	err := pool.Inner().QueryRow(ctx, `SHOW statement_timeout`).Scan(&raw)
	require.NoError(t, err)
	// PG normalizes "10000" to "10s". Accept either canonical form.
	require.Contains(t, []string{"10s", "10000"}, raw,
		"statement_timeout should be 10s, got %q", raw)
}

// --- Migrate ---

func TestMigrate_FreshHubAppliesAllSchema(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	for _, table := range []string{
		"sync_events", "sync_conflicts", "sync_projects",
		"applied_events", "audit_log",
	} {
		t.Run(table, func(t *testing.T) {
			var n int
			err := pool.Inner().QueryRow(context.Background(),
				`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename=$1`,
				table,
			).Scan(&n)
			require.NoError(t, err)
			require.Equal(t, 1, n, "%s must exist after Migrate", table)
		})
	}
}

func TestMigrate_Idempotent(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	require.NoError(t, pool.Migrate(context.Background()), "second Migrate is a no-op")
	require.NoError(t, pool.Migrate(context.Background()), "third Migrate is a no-op")
}

func TestMigrate_AdvisoryLockReleasedAtCommit(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	// After Migrate commits, the advisory lock must be released. Confirm
	// by acquiring it in a fresh tx and seeing immediate success.
	tx, err := pool.Inner().Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()

	var got bool
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT pg_try_advisory_xact_lock(hashtext($1))`,
		transport.AdvisoryLockKey,
	).Scan(&got))
	require.True(t, got, "lock must be free immediately after Migrate commits")
}

func TestMigrate_ConcurrentSingleFlight(t *testing.T) {
	dsn := requireTestDSN(t)
	freshSchema(t, dsn)

	const goroutines = 10
	pools := make([]*transport.Pool, goroutines)
	for i := range pools {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		p, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
		cancel()
		require.NoError(t, err)
		pools[i] = p
		t.Cleanup(p.Close)
	}

	var wg sync.WaitGroup
	var firstErr atomic.Value
	wg.Add(goroutines)
	start := make(chan struct{})
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := pools[i].Migrate(ctx); err != nil {
				firstErr.CompareAndSwap(nil, err)
			}
		}(i)
	}
	close(start)
	wg.Wait()

	if v := firstErr.Load(); v != nil {
		t.Fatalf("at least one concurrent Migrate failed: %v", v.(error))
	}

	// Final state: tables exist exactly once.
	var n int
	require.NoError(t, pools[0].Inner().QueryRow(context.Background(),
		`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename='sync_events'`,
	).Scan(&n))
	require.Equal(t, 1, n, "sync_events must exist exactly once")
}

// TestMigrate_PartialMigrationRecovery is the FR-18.7 chaos test:
// abort a Migrate mid-execution and verify the next Migrate runs
// cleanly. We simulate the abort by canceling the ctx after the
// advisory lock is held but before all SQL files have run.
func TestMigrate_PartialMigrationRecovery(t *testing.T) {
	pool := openTestPool(t)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	cancelCalled := make(chan struct{})
	go func() {
		// Cancel almost immediately — likely before all migrations
		// complete. PG rolls back; the advisory lock is released.
		time.Sleep(5 * time.Millisecond)
		cancel()
		close(cancelCalled)
	}()
	err := pool.Migrate(ctx)
	<-cancelCalled
	if err == nil {
		t.Skip("migrate completed before cancel landed; non-deterministic on fast hosts. Re-run for chaos coverage.")
	}
	require.True(t, errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded),
		"abort must surface as canceled/deadline, got: %v", err)

	// Recovery: a fresh Migrate must succeed cleanly.
	cleanCtx, cleanCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cleanCancel()
	require.NoError(t, pool.Migrate(cleanCtx),
		"after rollback, fresh Migrate must complete the schema")

	// Verify all tables exist after recovery.
	for _, table := range []string{"sync_events", "sync_conflicts", "applied_events", "audit_log"} {
		var n int
		require.NoError(t, pool.Inner().QueryRow(context.Background(),
			`SELECT count(*) FROM pg_tables WHERE schemaname='public' AND tablename=$1`,
			table,
		).Scan(&n))
		require.Equal(t, 1, n, "%s must exist after recovery", table)
	}
}

// TestSource_AcceptsTestDSNViaEnv is a sanity check that Source()
// returns the same DSN we use elsewhere — ties the source-resolution
// path to the integration-test path so a regression in either is caught.
func TestSource_AcceptsTestDSNViaEnv(t *testing.T) {
	dsn := requireTestDSN(t)
	dir := withMTIXDir(t)
	t.Setenv(transport.EnvDSN, dsn)
	got, err := transport.Source(dir)
	require.NoError(t, err)
	require.Equal(t, dsn, got)
}

// Sanity: ensure 'fmt' import isn't dropped by formatter when this
// test file's only fmt usage gets refactored later.
var _ = fmt.Sprintf
