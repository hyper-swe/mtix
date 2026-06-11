// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package benchmarks

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const envSyncPerfDSN = "MTIX_PG_TEST_DSN"

// throughputTarget is the FR-18 / MTIX-15.10 ceiling for both push
// and pull of 1000 events end-to-end.
const throughputTarget = 5 * time.Second

// requirePerfDSN returns the DSN or skips the benchmark/test.
func requirePerfDSN(t testing.TB) string {
	t.Helper()
	dsn := os.Getenv(envSyncPerfDSN)
	if dsn == "" {
		t.Skipf("set %s to enable sync throughput perf", envSyncPerfDSN)
	}
	return dsn
}

// freshHubForPerf drops the mtix-owned tables. Mirrors
// e2e/sync_e2e_test.freshHub. Inlined here so benchmarks/ doesn't
// import e2e (test packages can't share helpers cleanly).
func freshHubForPerf(t testing.TB, dsn string) {
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

// openHubForPerf opens a Pool against the test DSN and runs Migrate.
func openHubForPerf(t testing.TB) *transport.Pool {
	t.Helper()
	dsn := requirePerfDSN(t)
	freshHubForPerf(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Migrate(ctx))
	return pool
}

// newPerfStore opens a sqlite.Store and stamps a deterministic
// machine_hash so emitted events are valid.
func newPerfStore(t testing.TB, machineHash string) *sqlite.Store {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := sqlite.New(dir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })

	_, err = st.WriteDB().ExecContext(context.Background(),
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.machine_hash'`,
		machineHash)
	require.NoError(t, err)
	return st
}

// seedNodes creates n nodes via store.CreateNode (the production
// emit path), so n events queue in sync_events as pending.
func seedNodes(t testing.TB, st *sqlite.Store, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		node := makeNode("PRJ-"+strconv.Itoa(i+1), "perf seed")
		require.NoError(t, st.CreateNode(ctx, node))
	}
}

// pushAllPerf drains the local pending queue to the hub.
// Mirrors e2e/sync_e2e_test.fakeCLI.pushAll.
func pushAllPerf(ctx context.Context, t testing.TB, st *sqlite.Store, pool *transport.Pool) int {
	t.Helper()
	// Deliberately larger than production's pushBatchSize=100
	// (cmd/mtix/sync_push.go): the benchmark measures hub throughput
	// ceilings, not CLI batching policy. If push-timeout regressions are
	// suspected, rerun with batchSize=100 to mirror production.
	const batchSize = 500
	total := 0
	for {
		events := readPendingForPerf(ctx, t, st, batchSize)
		if len(events) == 0 {
			return total
		}
		acceptedIDs, conflicts, err := pool.PushEvents(ctx, events)
		require.NoError(t, err)
		// Synthetic single-author data must never produce hub conflicts;
		// a nonzero count means the harness or hub semantics drifted.
		require.Empty(t, conflicts, "unexpected hub conflicts in benchmark push")
		require.NoError(t, st.WithTx(ctx, func(tx *sql.Tx) error {
			for _, id := range acceptedIDs {
				if _, err := tx.ExecContext(ctx,
					`UPDATE sync_events SET sync_status = 'pushed' WHERE event_id = ?`,
					id); err != nil {
					return err
				}
			}
			return nil
		}))
		total += len(acceptedIDs)
	}
}

// pullAllPerf pulls and applies all hub events.
// Mirrors e2e/sync_e2e_test.fakeCLI.pullAll.
func pullAllPerf(ctx context.Context, t testing.TB, st *sqlite.Store, pool *transport.Pool) int {
	t.Helper()
	const batchSize = 500
	since := readCursorForPerf(ctx, t, st)
	total := 0
	for {
		events, hasMore, err := pool.PullEvents(ctx, since, batchSize)
		require.NoError(t, err)
		if len(events) == 0 {
			return total
		}
		require.NoError(t, st.WithTx(ctx, func(tx *sql.Tx) error {
			for _, e := range events {
				if applyErr := sqlite.IdempotentApply(ctx, tx, e); applyErr != nil {
					return applyErr
				}
			}
			return nil
		}))
		for _, e := range events {
			if e.LamportClock > since {
				since = e.LamportClock
			}
		}
		writeCursorForPerf(ctx, t, st, since)
		total += len(events)
		if !hasMore {
			return total
		}
	}
}

func readPendingForPerf(ctx context.Context, t testing.TB, st *sqlite.Store, limit int) []*model.SyncEvent {
	t.Helper()
	rows, err := st.Query(ctx, `
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		WHERE sync_status = 'pending'
		ORDER BY lamport_clock ASC
		LIMIT ?`, limit)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := make([]*model.SyncEvent, 0, limit)
	for rows.Next() {
		var e model.SyncEvent
		var opType, payload, vc string
		require.NoError(t, rows.Scan(
			&e.EventID, &e.ProjectPrefix, &e.NodeID, &opType, &payload,
			&e.WallClockTS, &e.LamportClock, &vc,
			&e.AuthorID, &e.AuthorMachineHash,
		))
		e.OpType = model.OpType(opType)
		e.Payload = json.RawMessage(payload)
		require.NoError(t, json.Unmarshal([]byte(vc), &e.VectorClock))
		out = append(out, &e)
	}
	require.NoError(t, rows.Err())
	return out
}

func readCursorForPerf(ctx context.Context, t testing.TB, st *sqlite.Store) int64 {
	t.Helper()
	var raw string
	require.NoError(t, st.QueryRow(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.last_pulled_clock'`,
	).Scan(&raw))
	v, err := strconv.ParseInt(raw, 10, 64)
	require.NoError(t, err)
	return v
}

func writeCursorForPerf(ctx context.Context, t testing.TB, st *sqlite.Store, cursor int64) {
	t.Helper()
	require.NoError(t, st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'meta.sync.last_pulled_clock'`,
			strconv.FormatInt(cursor, 10))
		return err
	}))
}

// TestPerf_PushPullTargets is the gating perf assertion: 1000 events
// push and pull each in under 5s. Default-skip gated behind
// MTIX_PERF_LONG=1 — race-detector overhead inflates the pull
// path from ~0.5s (un-instrumented) to ~6s on CI runners, false-
// failing the 5s production target. Operators run this in a
// dedicated perf CI job (no -race, MTIX_PERF_LONG=1 set). The
// BenchmarkSync* functions still emit ns/op without the gate.
func TestPerf_PushPullTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("perf throughput skipped under -short")
	}
	if os.Getenv("MTIX_PERF_LONG") != "1" {
		t.Skip("perf threshold assertions gated behind MTIX_PERF_LONG=1 (race overhead would false-fail)")
	}
	pool := openHubForPerf(t)
	ctx := context.Background()

	const n = 1000

	t.Run("push_1000_events", func(t *testing.T) {
		st := newPerfStore(t, "aaaaaaaaaaaaaaaa")
		seedNodes(t, st, n)

		start := time.Now()
		pushed := pushAllPerf(ctx, t, st, pool)
		elapsed := time.Since(start)

		require.Equal(t, n, pushed, "all events accepted")
		require.LessOrEqualf(t, elapsed, throughputTarget,
			"push of %d events: elapsed=%s; target=%s", n, elapsed, throughputTarget)
	})

	// Wipe the hub between subtests so the pull benchmark starts
	// from a known state.
	dsn := requirePerfDSN(t)
	freshHubForPerf(t, dsn)
	pool2, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool2.Close)
	require.NoError(t, pool2.Migrate(ctx))

	t.Run("pull_1000_events", func(t *testing.T) {
		// Producer A seeds + pushes.
		producer := newPerfStore(t, "aaaaaaaaaaaaaaaa")
		seedNodes(t, producer, n)
		require.Equal(t, n, pushAllPerf(ctx, t, producer, pool2))

		// Consumer B times pullAll.
		consumer := newPerfStore(t, "bbbbbbbbbbbbbbbb")
		start := time.Now()
		pulled := pullAllPerf(ctx, t, consumer, pool2)
		elapsed := time.Since(start)

		require.Equal(t, n, pulled, "all events pulled")
		require.LessOrEqualf(t, elapsed, throughputTarget,
			"pull of %d events: elapsed=%s; target=%s", n, elapsed, throughputTarget)
	})
}

// BenchmarkSyncPush_1000Events times pushAll over 1000 pre-seeded
// pending events. Each iteration uses a fresh sqlite.Store + fresh
// hub because PushEvents is not idempotent across a single Pool's
// view (re-running the same b.N iteration on a non-fresh hub would
// short-circuit via ON CONFLICT DO NOTHING).
func BenchmarkSyncPush_1000Events(b *testing.B) {
	dsn := requirePerfDSN(b)
	ctx := context.Background()

	for i := 0; i < b.N; i++ {
		freshHubForPerf(b, dsn)
		pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
		require.NoError(b, err)
		require.NoError(b, pool.Migrate(ctx))

		st := newPerfStore(b, "aaaaaaaaaaaaaaaa")
		seedNodes(b, st, 1000)

		b.StartTimer()
		pushed := pushAllPerf(ctx, b, st, pool)
		b.StopTimer()

		require.Equal(b, 1000, pushed)
		pool.Close()
	}
}

// BenchmarkSyncPull_1000Events times pullAll on a hub pre-loaded
// with 1000 events. Setup time (push from the producer) is excluded
// via b.StopTimer / b.StartTimer.
func BenchmarkSyncPull_1000Events(b *testing.B) {
	dsn := requirePerfDSN(b)
	ctx := context.Background()

	b.StopTimer()
	for i := 0; i < b.N; i++ {
		freshHubForPerf(b, dsn)
		pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
		require.NoError(b, err)
		require.NoError(b, pool.Migrate(ctx))

		producer := newPerfStore(b, "aaaaaaaaaaaaaaaa")
		seedNodes(b, producer, 1000)
		require.Equal(b, 1000, pushAllPerf(ctx, b, producer, pool))

		consumer := newPerfStore(b, "bbbbbbbbbbbbbbbb")
		b.StartTimer()
		pulled := pullAllPerf(ctx, b, consumer, pool)
		b.StopTimer()

		require.Equal(b, 1000, pulled)
		pool.Close()
	}
}

// BenchmarkSyncPushPullRoundTrip_100Events is the small-batch
// benchmark for tighter dev-iteration feedback. 100 events round
// trip end-to-end.
func BenchmarkSyncPushPullRoundTrip_100Events(b *testing.B) {
	dsn := requirePerfDSN(b)
	ctx := context.Background()

	b.StopTimer()
	for i := 0; i < b.N; i++ {
		freshHubForPerf(b, dsn)
		pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
		require.NoError(b, err)
		require.NoError(b, pool.Migrate(ctx))

		producer := newPerfStore(b, "aaaaaaaaaaaaaaaa")
		consumer := newPerfStore(b, "bbbbbbbbbbbbbbbb")
		seedNodes(b, producer, 100)

		b.StartTimer()
		require.Equal(b, 100, pushAllPerf(ctx, b, producer, pool))
		require.Equal(b, 100, pullAllPerf(ctx, b, consumer, pool))
		b.StopTimer()

		pool.Close()
	}
}
