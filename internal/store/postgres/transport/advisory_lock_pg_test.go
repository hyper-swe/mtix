// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// heldSessionLocks counts the granted advisory locks on AdvisoryLockKey in
// the current database, read from pg_locks on a connection of its own
// (MTIX-107.85). A bigint advisory lock splits its key into classid (high
// 32 bits) and objid (low 32 bits).
func heldSessionLocks(t *testing.T, dsn string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close(ctx)) }()
	var n int
	require.NoError(t, conn.QueryRow(ctx, `
		SELECT count(*) FROM pg_catalog.pg_locks l
		JOIN pg_catalog.pg_database d ON d.oid = l.database
		WHERE l.locktype = 'advisory' AND l.granted
		  AND d.datname = pg_catalog.current_database()
		  AND l.pid <> pg_catalog.pg_backend_pid()
		  AND ((l.classid::bigint << 32) | l.objid::bigint) = hashtext($1)::bigint`,
		transport.AdvisoryLockKey).Scan(&n))
	return n
}

// TestEnsureRegistryIndex_CancelledWhileLocked_ReleasesSessionLock cancels
// the caller's context while EnsureRegistryIndex holds its session advisory
// lock, then proves no connection, pooled or not, still holds that lock and
// the pool can take it again (MTIX-107.85).
func TestEnsureRegistryIndex_CancelledWhileLocked_ReleasesSessionLock(t *testing.T) {
	dsn := requireTestDSN(t)
	migratedPoolWithoutIndex(t)
	pool := poolFor(t, nil)
	openGate(t, pool, "MTIX")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	transport.SetAfterSessionLockForTest(pool, cancel)

	_, err := pool.EnsureRegistryIndex(ctx, "MTIX")
	require.ErrorContains(t, err, "context canceled")
	require.Zero(t, heldSessionLocks(t, dsn),
		"no pooled connection keeps the session advisory lock after a cancelled run")

	transport.SetAfterSessionLockForTest(pool, nil)
	again, againCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer againCancel()
	res, err := pool.EnsureRegistryIndex(again, "MTIX")
	require.NoError(t, err, "a later run on the same pool takes the lock again")
	require.True(t, res.Added)
	require.Zero(t, heldSessionLocks(t, dsn))
}

// TestReleaseSessionLock_LockNotHeld_ClosesConnection releases a session
// lock the connection does not hold, so pg_advisory_unlock returns false,
// and proves the failure is reported and the connection is closed rather
// than returned to the pool (MTIX-107.85).
func TestReleaseSessionLock_LockNotHeld_ClosesConnection(t *testing.T) {
	requireTestDSN(t)
	pool := poolFor(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	closed, err := transport.ReleaseUnheldSessionLockForTest(ctx, pool)
	require.ErrorContains(t, err, "did not hold the lock")
	require.True(t, closed, "a connection whose unlock failed is closed")
	require.NoError(t, pool.HealthCheck(ctx), "the pool opens a new connection")
}
