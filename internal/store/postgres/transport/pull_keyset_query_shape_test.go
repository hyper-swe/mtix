// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// Query shape of the pull cursor pass (MTIX-95.4; ADR-006 D39): with hub
// migration 015 applied, the planner can serve the keyset query from
// idx_sync_events_lamport_event_id, bounded by the cursor, in index order,
// with no sequential scan and no sort of the whole log per page. On a small
// test table the planner prefers a scan and a sort, so, like the late-event
// query-shape tests, the EXPLAIN turns the alternatives off for its
// transaction only (sequential and bitmap scans, and sorts): the question
// is whether the index CAN serve the page in order. A plan that still
// holds a Sort or a Seq Scan means it cannot. Skips when MTIX_PG_TEST_DSN
// is unset.

// explainPullEvents is a compile-time constant built from the exact SQL
// PullEvents runs; no value is ever placed in the SQL text.
const explainPullEvents = "EXPLAIN " + pullEventsSQL

// explainIndexOrderOnly runs one EXPLAIN statement with args and returns the
// plan text, with sequential scans, bitmap scans and sorts disabled for the
// transaction.
func explainIndexOrderOnly(t *testing.T, pool *Pool, explain string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.p.Begin(ctx)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback(ctx) }()
	for _, stmt := range []string{
		`SET LOCAL enable_seqscan = off`,
		`SET LOCAL enable_bitmapscan = off`,
		`SET LOCAL enable_sort = off`,
	} {
		_, err = tx.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	rows, err := tx.Query(ctx, explain, args...)
	require.NoError(t, err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	return strings.Join(lines, "\n")
}

// TestPullEvents_PlanUsesKeysetIndex_NoSeqScanNoSort asserts the pull page
// can be read from the keyset index starting at the cursor (the Index Cond
// is the row comparison), in the index's order (no Sort).
func TestPullEvents_PlanUsesKeysetIndex_NoSeqScanNoSort(t *testing.T) {
	pool := queryShapePool(t)
	tests := []struct {
		name string
		args []any
	}{
		{"first page from the zero cursor", []any{int64(0), "", 1001}},
		{"a later page from a full cursor", []any{int64(42), "0193fc00-0000-7000-8000-000000000001", 1001}},
	}
	cond := regexp.MustCompile(
		`Index Scan using idx_sync_events_lamport_event_id[^\n]*\n\s+Index Cond: \(ROW\(lamport_clock, event_id\) > ROW\(`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := explainIndexOrderOnly(t, pool, explainPullEvents, tt.args...)
			require.Regexp(t, cond, plan)
			require.NotContains(t, plan, "Seq Scan on sync_events", "plan:\n%s", plan)
			require.NotContains(t, plan, "Sort", "plan:\n%s", plan)
		})
	}
}
