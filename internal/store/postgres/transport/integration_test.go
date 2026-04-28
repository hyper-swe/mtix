// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
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

// --- PushEvents / PullEvents ---

func makeEvent(id, nodeID, author string, lamport int64) *model.SyncEvent {
	return &model.SyncEvent{
		EventID:           id,
		ProjectPrefix:     "MTIX",
		NodeID:            nodeID,
		OpType:            model.OpCreateNode,
		Payload:           json.RawMessage(`{"title":"x"}`),
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      lamport,
		VectorClock:       model.VectorClock{author: lamport},
		AuthorID:          author,
		AuthorMachineHash: "0123456789abcdef",
	}
}

func TestPushEvents_HappyPath(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	events := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-000000000001", "MTIX-1", "alice", 1),
		makeEvent("0193fa00-0000-7000-8000-000000000002", "MTIX-2", "alice", 2),
		makeEvent("0193fa00-0000-7000-8000-000000000003", "MTIX-3", "alice", 3),
	}
	ids, conflicts, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)
	require.Len(t, ids, 3)
	require.Empty(t, conflicts)
}

func TestPushEvents_RejectsBatchOnInvalidEvent(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	events := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-000000000001", "MTIX-1", "alice", 1),
		// Future timestamp — invalid.
		func() *model.SyncEvent {
			e := makeEvent("0193fa00-0000-7000-8000-000000000002", "MTIX-2", "alice", 2)
			e.WallClockTS = time.Now().Add(48 * time.Hour).UnixMilli()
			return e
		}(),
		makeEvent("0193fa00-0000-7000-8000-000000000003", "MTIX-3", "alice", 3),
	}
	_, _, err := pool.PushEvents(context.Background(), events)
	require.Error(t, err)

	// Verify NO partial writes — sync_events is empty.
	var n int
	require.NoError(t, pool.Inner().QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events`).Scan(&n))
	require.Equal(t, 0, n, "invalid batch must not partially apply")
}

func TestPushEvents_IdempotentOnRepush(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	events := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-000000000001", "MTIX-1", "alice", 1),
		makeEvent("0193fa00-0000-7000-8000-000000000002", "MTIX-2", "alice", 2),
	}
	ids, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)
	require.Len(t, ids, 2)

	ids2, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)
	require.Empty(t, ids2, "re-push of same events accepts none (ON CONFLICT DO NOTHING)")
}

func TestPullEvents_FromZero(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	events := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-000000000001", "MTIX-1", "alice", 1),
		makeEvent("0193fa00-0000-7000-8000-000000000002", "MTIX-2", "alice", 2),
		makeEvent("0193fa00-0000-7000-8000-000000000003", "MTIX-3", "alice", 3),
	}
	_, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)

	got, hasMore, err := pool.PullEvents(context.Background(), 0, 100)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.False(t, hasMore)
}

func TestPullEvents_FromHighWaterMark(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	events := make([]*model.SyncEvent, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("0193fa00-0000-7000-8000-00000000000%d", i+1)
		events[i] = makeEvent(id, fmt.Sprintf("MTIX-%d", i+1), "alice", int64(i+1))
	}
	_, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)

	got, hasMore, err := pool.PullEvents(context.Background(), 2, 100)
	require.NoError(t, err)
	require.Len(t, got, 3, "lamport > 2: events 3, 4, 5")
	require.False(t, hasMore)
	for _, e := range got {
		require.Greater(t, e.LamportClock, int64(2))
	}
}

func TestPullEvents_HasMoreFlag(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	events := make([]*model.SyncEvent, 5)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("0193fa00-0000-7000-8000-00000000000%d", i+1)
		events[i] = makeEvent(id, fmt.Sprintf("MTIX-%d", i+1), "alice", int64(i+1))
	}
	_, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)

	got, hasMore, err := pool.PullEvents(context.Background(), 0, 3)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.True(t, hasMore, "5 events on hub, limit 3 -> hasMore=true")
}

func TestPullEvents_OrderedByLamport(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	// Push out-of-order lamports.
	events := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-000000000001", "MTIX-1", "alice", 5),
		makeEvent("0193fa00-0000-7000-8000-000000000002", "MTIX-2", "alice", 1),
		makeEvent("0193fa00-0000-7000-8000-000000000003", "MTIX-3", "alice", 3),
	}
	_, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)

	got, _, err := pool.PullEvents(context.Background(), 0, 100)
	require.NoError(t, err)
	require.Len(t, got, 3)
	require.Equal(t, int64(1), got[0].LamportClock)
	require.Equal(t, int64(3), got[1].LamportClock)
	require.Equal(t, int64(5), got[2].LamportClock)
}

func TestPushEvents_DetectsConcurrentEdits(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	// First event from alice for MTIX-1 / title.
	first := &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000001",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpUpdateField,
		Payload:           json.RawMessage(`{"field_name":"title","new_value":"\"alice-title\""}`),
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	_, _, err := pool.PushEvents(context.Background(), []*model.SyncEvent{first})
	require.NoError(t, err)

	// Concurrent event from bob for the same node — VC has bob:1 only,
	// so VectorClock.Concurrent(alice's VC) returns true.
	concurrent := &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000002",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpUpdateField,
		Payload:           json.RawMessage(`{"field_name":"title","new_value":"\"bob-title\""}`),
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"bob": 1},
		AuthorID:          "bob",
		AuthorMachineHash: "fedcba9876543210",
	}
	_, conflicts, err := pool.PushEvents(context.Background(), []*model.SyncEvent{concurrent})
	require.NoError(t, err)
	require.Len(t, conflicts, 1, "concurrent edit detected")
	require.Equal(t, "0193fa00-0000-7000-8000-000000000002", conflicts[0].NewEventID)
	require.Equal(t, "0193fa00-0000-7000-8000-000000000001", conflicts[0].ConflictingEventID)
	require.Equal(t, "MTIX-1", conflicts[0].NodeID)
	require.Equal(t, "title", conflicts[0].FieldName)
}

// MTIX-15.5.2: hub-side conflict persistence. After PushEvents
// detects a conflict, the row is recorded in sync_conflicts atomically
// with the event INSERT. Triggers from 006_triggers.sql then prevent
// any UPDATE/DELETE on that row.

func TestPushEvents_ConflictPersistedToHub(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	first := &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000301",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpUpdateField,
		Payload:           json.RawMessage(`{"field_name":"title","new_value":"\"alice-title\""}`),
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"alice": 1},
		AuthorID:          "alice",
		AuthorMachineHash: "0123456789abcdef",
	}
	_, _, err := pool.PushEvents(context.Background(), []*model.SyncEvent{first})
	require.NoError(t, err)

	concurrent := &model.SyncEvent{
		EventID:           "0193fa00-0000-7000-8000-000000000302",
		ProjectPrefix:     "MTIX",
		NodeID:            "MTIX-1",
		OpType:            model.OpUpdateField,
		Payload:           json.RawMessage(`{"field_name":"title","new_value":"\"bob-title\""}`),
		WallClockTS:       time.Now().UnixMilli(),
		LamportClock:      1,
		VectorClock:       model.VectorClock{"bob": 1},
		AuthorID:          "bob",
		AuthorMachineHash: "fedcba9876543210",
	}
	_, conflicts, err := pool.PushEvents(context.Background(), []*model.SyncEvent{concurrent})
	require.NoError(t, err)
	require.Len(t, conflicts, 1)

	// Conflict persisted to hub.sync_conflicts atomically with the event INSERT.
	var stored struct {
		EventA, EventB, NodeID, Field, Resolution string
	}
	require.NoError(t, pool.Inner().QueryRow(context.Background(), `
		SELECT event_id_a, event_id_b, node_id, field_name, resolution
		FROM sync_conflicts WHERE event_id_a = $1`,
		concurrent.EventID,
	).Scan(&stored.EventA, &stored.EventB, &stored.NodeID, &stored.Field, &stored.Resolution))
	require.Equal(t, concurrent.EventID, stored.EventA)
	require.Equal(t, first.EventID, stored.EventB)
	require.Equal(t, "MTIX-1", stored.NodeID)
	require.Equal(t, "title", stored.Field)
	require.Equal(t, "lww", stored.Resolution)
}

func TestPushEvents_ConflictsTriggerRefusesUpdate(t *testing.T) {
	// FR-18.5: sync_conflicts is append-only. The trigger from
	// 006_triggers.sql raises an exception on UPDATE/DELETE.
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	// Manually insert a conflict row (bypassing PushEvents to keep the
	// test focused on the trigger behavior).
	_, err := pool.Inner().Exec(context.Background(), `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash)
		VALUES
		  ('e-a', 'MTIX', 'MTIX-1', 'update_field', '{"field_name":"title","new_value":"\"x\""}',
		   1, 1, '{"alice":1}', 'alice', '0123456789abcdef'),
		  ('e-b', 'MTIX', 'MTIX-1', 'update_field', '{"field_name":"title","new_value":"\"y\""}',
		   2, 2, '{"bob":1}', 'bob', 'fedcba9876543210')`)
	require.NoError(t, err)
	_, err = pool.Inner().Exec(context.Background(), `
		INSERT INTO sync_conflicts (event_id_a, event_id_b, node_id, field_name, resolution)
		VALUES ('e-a', 'e-b', 'MTIX-1', 'title', 'lww')`)
	require.NoError(t, err)

	_, err = pool.Inner().Exec(context.Background(),
		`UPDATE sync_conflicts SET resolution = 'manual' WHERE event_id_a = 'e-a'`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "append-only",
		"trigger MUST raise FR-18.5 append-only exception")

	_, err = pool.Inner().Exec(context.Background(),
		`DELETE FROM sync_conflicts WHERE event_id_a = 'e-a'`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "append-only")
}

// Keep fmt referenced (used by helpers).
var _ = fmt.Sprintf
