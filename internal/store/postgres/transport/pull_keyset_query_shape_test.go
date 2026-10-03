// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// pullEventsPlanName names the session-level prepared statement that holds
// the exact SQL PullEvents runs (pullEventsSQL), so each case EXPLAINs it
// with a literal EXECUTE statement.
const pullEventsPlanName = "pull_events_plan"

// deallocatePullEvents is a fully literal statement (no concatenation).
const deallocatePullEvents = `DEALLOCATE pull_events_plan`

// explainIndexOrderOnly prepares pullEventsSQL as pullEventsPlanName with a
// SQL PREPARE built server-side by format() from bound arguments (SQL Rule
// 1a), runs the literal statement explain (an EXPLAIN EXECUTE of that
// plan) and returns the plan text, with sequential scans, bitmap scans and
// sorts disabled for the transaction. A SQL PREPARE outlives a rolled back
// transaction, so a deferred DEALLOCATE on the same connection (ignoring
// SQLSTATE 26000, "does not exist") keeps a failure from leaking the
// statement into the next case on a pooled connection.
func explainIndexOrderOnly(t *testing.T, pool *Pool, explain string) string {
	t.Helper()
	ctx := context.Background()
	conn, err := pool.p.Acquire(ctx)
	require.NoError(t, err)
	defer conn.Release()
	tx, err := conn.Begin(ctx)
	require.NoError(t, err)
	defer func() {
		// Roll back, then clean up on the same connection.
		_ = tx.Rollback(ctx)
		if _, derr := conn.Exec(ctx, deallocatePullEvents); derr != nil {
			var pgErr *pgconn.PgError
			if !errors.As(derr, &pgErr) || pgErr.Code != "26000" {
				t.Errorf("deallocate %s: %v", pullEventsPlanName, derr)
			}
		}
	}()
	for _, stmt := range []string{
		`SET LOCAL enable_seqscan = off`,
		`SET LOCAL enable_bitmapscan = off`,
		`SET LOCAL enable_sort = off`,
	} {
		_, err = tx.Exec(ctx, stmt)
		require.NoError(t, err, stmt)
	}
	// SQL PREPARE, not the protocol-level Prepare: a connection pooler may
	// rename protocol-level statements, so EXPLAIN EXECUTE could not find
	// them (SQLSTATE 26000). Both run in this transaction on one session.
	var prepare string
	require.NoError(t, tx.QueryRow(ctx,
		`SELECT format('PREPARE %I AS %s', $1::text, $2::text)`,
		pullEventsPlanName, pullEventsSQL).Scan(&prepare))
	_, err = tx.Exec(ctx, prepare)
	require.NoError(t, err)
	rows, err := tx.Query(ctx, explain)
	require.NoError(t, err)
	lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)
	_, err = tx.Exec(ctx, deallocatePullEvents)
	require.NoError(t, err)
	return strings.Join(lines, "\n")
}

// TestPullEvents_PlanUsesKeysetIndex_NoSeqScanNoSort asserts the pull page
// can be read from the keyset index starting at the cursor (the Index Cond
// is the row comparison), in the index's order (no Sort).
func TestPullEvents_PlanUsesKeysetIndex_NoSeqScanNoSort(t *testing.T) {
	pool := queryShapePool(t)
	tests := []struct {
		name    string
		explain string
	}{
		{"first page from the zero cursor", `EXPLAIN EXECUTE pull_events_plan(0, '', 1001)`},
		{"a later page from a full cursor",
			`EXPLAIN EXECUTE pull_events_plan(42, '0193fc00-0000-7000-8000-000000000001', 1001)`},
	}
	cond := regexp.MustCompile(
		`Index Scan using idx_sync_events_lamport_event_id[^\n]*\n\s+Index Cond: \(ROW\(lamport_clock, event_id\) > ROW\(`)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := explainIndexOrderOnly(t, pool, tt.explain)
			require.Regexp(t, cond, plan)
			require.NotContains(t, plan, "Seq Scan on sync_events", "plan:\n%s", plan)
			require.NotContains(t, plan, "Sort", "plan:\n%s", plan)
		})
	}
}
