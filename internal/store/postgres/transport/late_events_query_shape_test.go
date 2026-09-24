// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Query-shape tests for the late-event sweep reads (MTIX-95.5 acceptance
// 5). They EXPLAIN the exact SQL the sweep runs, against a migrated hub,
// and assert the planner can serve each query from an index: the id
// listing (a window, or the full history from the zero cursor) from
// migration 014's created_at index, and the fetch by id from the primary
// key. On a small test table the
// planner prefers a sequential scan, so each EXPLAIN runs with
// enable_seqscan off for its transaction only: the question is whether an
// index CAN serve the query, which is what keeps a large hub's per-pull
// sweep off a full table scan. Skips when MTIX_PG_TEST_DSN is unset.

// queryShapePool opens a pool on the test DSN and migrates a clean hub.
// The DSN must point at a throwaway database: the mtix tables are dropped.
func queryShapePool(t *testing.T) *Pool {
	t.Helper()
	dsn := os.Getenv("MTIX_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set MTIX_PG_TEST_DSN to enable the late-event query-shape tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := New(ctx, dsn, Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS sync_node_collisions CASCADE`,
		`DROP TABLE IF EXISTS node_renumber_remaps CASCADE`,
		`DROP TABLE IF EXISTS sync_conflicts CASCADE`,
		`DROP TABLE IF EXISTS applied_events CASCADE`,
		`DROP TABLE IF EXISTS sync_events CASCADE`,
		`DROP TABLE IF EXISTS sync_project_clients CASCADE`,
		`DROP TABLE IF EXISTS sync_projects CASCADE`,
		`DROP TABLE IF EXISTS sync_hub_state CASCADE`,
		`DROP TABLE IF EXISTS audit_log CASCADE`,
		`DROP FUNCTION IF EXISTS audit_log_immutable() CASCADE`,
	} {
		_, err := pool.p.Exec(ctx, stmt)
		require.NoErrorf(t, err, "drop: %s", stmt)
	}
	require.NoError(t, pool.Migrate(ctx))
	return pool
}

// The EXPLAIN statements are compile-time constants built from the exact
// SQL the sweep runs; no value is ever placed in the SQL text.
const (
	explainListEventIDsSince = "EXPLAIN " + listEventIDsSinceSQL
	explainFetchEventsByID   = "EXPLAIN " + fetchEventsByIDSQL
)

// explainWithoutSeqScan runs one EXPLAIN statement with args and returns
// the plan text, with sequential scans disabled for the transaction.
func explainWithoutSeqScan(t *testing.T, pool *Pool, explain string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.p.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `SET LOCAL enable_seqscan = off`)
	require.NoError(t, err)
	rows, err := tx.Query(ctx, explain, args...)
	require.NoError(t, err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return strings.Join(lines, "\n")
}

// TestLateEventQueries_PlanUsesIndex_ServesSweepWithoutSeqScan asserts
// each sweep query can be answered from its index, with the index
// condition on the query's own predicate. Naming the index is not enough:
// an ORDER BY created_at can walk idx_sync_events_created_at from its
// oldest entry and filter every row, which is a full scan in index order.
// The plan must show the index scan followed by an Index Cond on the
// predicate that bounds the scan (created_at >= the cursor's created_at,
// or the event_id key).
func TestLateEventQueries_PlanUsesIndex_ServesSweepWithoutSeqScan(t *testing.T) {
	pool := queryShapePool(t)
	windowStart := time.Now().Add(-15 * time.Minute)
	tests := []struct {
		name    string
		explain string
		args    []any
		cond    *regexp.Regexp
	}{
		{"window listing is bounded by the created_at index", explainListEventIDsSince,
			[]any{windowStart, "", 1001},
			regexp.MustCompile(`idx_sync_events_created_at[^\n]*\n\s+Index Cond: \(+created_at >= `)},
		{"full-history listing from the zero cursor is served by the created_at index",
			explainListEventIDsSince, []any{time.Time{}, "", 1001},
			regexp.MustCompile(`idx_sync_events_created_at[^\n]*\n\s+Index Cond: \(+created_at >= `)},
		{"fetch by id is served by the primary key", explainFetchEventsByID,
			[]any{[]string{"0193fb00-0000-7000-8000-000000000001"}},
			regexp.MustCompile(`sync_events_pkey[^\n]*\n\s+Index Cond: \(+event_id = ANY `)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := explainWithoutSeqScan(t, pool, tt.explain, tt.args...)
			require.Regexp(t, tt.cond, plan)
			require.NotContains(t, plan, "Seq Scan on sync_events", "plan:\n%s", plan)
		})
	}
}
