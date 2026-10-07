// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// The disposable-DB fixture blocks later migration DDL while earlier new
// relations exist in the uncommitted transaction. No production hooks are used.
type migrationRecoveryFixture struct {
	ctx        context.Context
	observer   *pgx.Conn
	blocker    pgx.Tx
	blockerPID int
}

func newRecoveryConnection(ctx context.Context, t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(ctx, requireTestDSN(t))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, conn.Close(context.Background())) })
	return conn
}

func newMigrationRecoveryFixture(t *testing.T) *migrationRecoveryFixture {
	t.Helper()
	// This bounds observation and cleanup failures; it never schedules cancel.
	ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(stop)
	observer := newRecoveryConnection(ctx, t)
	ddl, err := migrations.Read("005_audit_log.sql")
	require.NoError(t, err)
	// SQL Rule1a: exact compiled-in DDL, followed by fixed fixture statements.
	_, err = observer.Exec(ctx, ddl)
	require.NoError(t, err)
	_, err = observer.Exec(ctx, `DROP INDEX public.idx_audit_log_project_time`)
	require.NoError(t, err)
	blockerConn := newRecoveryConnection(ctx, t)
	tx, err := blockerConn.Begin(ctx)
	require.NoError(t, err)
	f := &migrationRecoveryFixture{ctx: ctx, observer: observer, blocker: tx}
	t.Cleanup(func() { f.releaseBlocker(t) })
	require.NoError(t, tx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&f.blockerPID))
	// Holding this relation lock forces the real later schema/index execution
	// to wait after the earlier sync tables have been created transactionally.
	_, err = tx.Exec(ctx, `LOCK TABLE public.audit_log IN ACCESS EXCLUSIVE MODE`)
	require.NoError(t, err)
	return f
}

func (f *migrationRecoveryFixture) joinOnCleanup(t *testing.T, cancel context.CancelFunc, finished <-chan struct{}) {
	t.Helper()
	t.Cleanup(func() {
		cancel()
		ctx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		select {
		case <-finished:
		case <-ctx.Done():
			t.Error("canceled migration goroutine did not finish")
		}
	})
}

func (f *migrationRecoveryFixture) waitForSchemaLock(t *testing.T) int {
	t.Helper()
	for {
		var pid int
		var mode string
		// The blocked backend must hold the exact bigint advisory key used by
		// Migrate and wait on OUR audit relation lock, not another migration.
		err := f.observer.QueryRow(f.ctx, `
   SELECT waiting.pid, waiting.mode FROM pg_locks waiting
   WHERE waiting.locktype='relation' AND NOT waiting.granted
    AND waiting.relation='public.audit_log'::regclass
    AND $1=ANY(pg_blocking_pids(waiting.pid))
    AND EXISTS (SELECT 1 FROM pg_locks held
     WHERE held.pid=waiting.pid AND held.locktype='advisory' AND held.granted
      AND held.classid=((hashtext($2)::bigint >> 32) & 4294967295)::oid
      AND held.objid=(hashtext($2)::bigint & 4294967295)::oid AND held.objsubid=1)
   LIMIT 1`, f.blockerPID, transport.AdvisoryLockKey).Scan(&pid, &mode)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		require.NoError(t, err, "bounded observation must see actual schema wait")
		t.Logf("observed migration backend=%d blocker=%d audit wait=%s", pid, f.blockerPID, mode)
		f.assertNewRelationLocks(t, pid)
		return pid
	}
}

func (f *migrationRecoveryFixture) assertNewRelationLocks(t *testing.T, pid int) {
	t.Helper()
	// Earlier newly-created tables/indexes carry granted exclusive relation
	// locks, even though their catalog rows are invisible outside the tx.
	rows, err := f.observer.Query(f.ctx, `SELECT relation::text, mode FROM pg_locks
  WHERE pid=$1 AND locktype='relation' AND granted AND mode='AccessExclusiveLock'
  ORDER BY relation, mode`, pid)
	require.NoError(t, err)
	defer rows.Close()
	var locks []string
	for rows.Next() {
		var oid, mode string
		require.NoError(t, rows.Scan(&oid, &mode))
		locks = append(locks, oid+":"+mode)
	}
	require.NoError(t, rows.Err())
	require.GreaterOrEqual(t, len(locks), 4, "schema transaction must have created earlier relations")
	t.Logf("uncommitted new relation locks=%v", locks)
}

func (f *migrationRecoveryFixture) migrationResult(t *testing.T, result <-chan error) error {
	t.Helper()
	select {
	case err := <-result:
		return err
	case <-f.ctx.Done():
		t.Fatal("observed migration did not return after explicit cancellation")
		return f.ctx.Err()
	}
}

func (f *migrationRecoveryFixture) waitForAdvisoryRelease(t *testing.T, pid int) {
	t.Helper()
	for {
		var held bool
		// Client cancellation may return just before the backend finishes its
		// rollback. Observe that exact backend's release with a bounded context.
		require.NoError(t, f.observer.QueryRow(f.ctx, `SELECT EXISTS (
   SELECT 1 FROM pg_locks WHERE pid=$1 AND locktype='advisory' AND granted)`, pid).Scan(&held))
		if !held {
			return
		}
	}
}

func (f *migrationRecoveryFixture) assertPartialRollback(t *testing.T) {
	t.Helper()
	for _, table := range []string{"sync_events", "sync_conflicts", "sync_projects", "applied_events"} {
		var n int
		// Catalog reads do not wait behind the fixture's audit relation lock.
		require.NoError(t, f.observer.QueryRow(f.ctx, `SELECT count(*) FROM pg_tables
   WHERE schemaname='public' AND tablename=$1`, table).Scan(&n))
		require.Zero(t, n, "%s created before the gate must roll back", table)
	}
	var n int
	require.NoError(t, f.observer.QueryRow(f.ctx, `SELECT count(*) FROM pg_tables
  WHERE schemaname='public' AND tablename='audit_log'`).Scan(&n))
	require.Equal(t, 1, n, "pre-existing seed survives rollback")
	f.assertAuditIndex(t, false)
}

func (f *migrationRecoveryFixture) assertAuditIndex(t *testing.T, present bool) {
	t.Helper()
	var n int
	// The missing fixture index must remain absent on abort, then be repaired.
	require.NoError(t, f.observer.QueryRow(f.ctx, `SELECT count(*) FROM pg_indexes
  WHERE schemaname='public' AND indexname='idx_audit_log_project_time'`).Scan(&n))
	if present {
		require.Equal(t, 1, n)
	} else {
		require.Zero(t, n)
	}
}

func (f *migrationRecoveryFixture) assertAdvisoryFree(t *testing.T) {
	t.Helper()
	tx, err := f.observer.Begin(f.ctx)
	require.NoError(t, err)
	defer func() { require.NoError(t, tx.Rollback(context.Background())) }()
	var acquired bool
	// An independent transaction verifies availability before and after repair.
	require.NoError(t, tx.QueryRow(f.ctx, `SELECT pg_try_advisory_xact_lock(hashtext($1))`,
		transport.AdvisoryLockKey).Scan(&acquired))
	require.True(t, acquired, "migration transaction must release its advisory lock")
}

func (f *migrationRecoveryFixture) releaseBlocker(t *testing.T) {
	t.Helper()
	err := f.blocker.Rollback(context.Background())
	if err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Errorf("release fixture relation lock: %v", err)
	}
}

func (f *migrationRecoveryFixture) recover(t *testing.T) *transport.Pool {
	t.Helper()
	pool, err := transport.New(f.ctx, requireTestDSN(t), transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Migrate(f.ctx), "fresh migration must restore the partial schema")
	return pool
}
