// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planOf returns the query plan of one of the package's own constant
// statements (CLAUDE.md Rule 1b: a test may prefix EXPLAIN QUERY PLAN to a
// constant from the package's own source, with the arguments still bound).
func planOf(t *testing.T, s *Store, query string, args ...any) string {
	t.Helper()
	rows, err := s.readDB.Query("EXPLAIN QUERY PLAN "+query, args...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var out []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		require.NoError(t, rows.Scan(&id, &parent, &unused, &detail))
		out = append(out, detail)
	}
	require.NoError(t, rows.Err())
	return strings.Join(out, "\n")
}

func TestHistorySize_Queries_UseIndexesAndNeverScanNodes(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	for name, q := range map[string]string{"largest": historyLargestSQL, "by uid": historyIDByUIDSQL, "by id": historyIDByIDSQL} {
		args := []any{"x"}
		if name == "largest" {
			args = []any{5}
		}
		plan := planOf(t, s, q, args...)
		assert.NotContains(t, plan, "SCAN nodes", "%s: %s", name, plan)
	}
	assert.Contains(t, planOf(t, s, historyIDByUIDSQL, "x"), "idx_nodes_uid")
	assert.Contains(t, planOf(t, s, historyLargestSQL, 5), "idx_task_history_uid_ts")
}

// A 20k-task board with 100k history rows reports its size in bounded time.
func TestHistorySize_LargeBoard_FinishesInBoundedTime(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	ctx := context.Background()
	_, err = s.writeDB.ExecContext(ctx, `WITH RECURSIVE n(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM n WHERE i < 20000)
		INSERT INTO nodes (id, parent_id, depth, seq, project, title, node_type, priority, status, progress, weight,
		  created_at, updated_at, uid, content_hash)
		SELECT 'BIG-' || i, NULL, 0, i, 'BIG', 't', 'epic', 3, 'open', 0, 1, '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z',
		  printf('00000000-0000-4000-8000-%012d', i), 'h' FROM n`)
	require.NoError(t, err)
	_, err = s.writeDB.ExecContext(ctx, `WITH RECURSIVE h(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM h WHERE i < 100000)
		INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field)
		SELECT printf('E%025d', i), printf('00000000-0000-4000-8000-%012d', i % 20000 + 1),
		  printf('2026-01-01T00:00:00.%09dZ', i), 'a', 'local', 'change', 'status' FROM h`)
	require.NoError(t, err)
	start := time.Now()
	rep, err := s.HistorySize(ctx, 5, 3)
	require.NoError(t, err)
	assert.Equal(t, 100000, rep.Rows)
	assert.Equal(t, 5, len(rep.Largest))
	assert.True(t, strings.HasPrefix(rep.Largest[0].ID, "BIG-"), fmt.Sprint(rep.Largest[0]))
	// The plan test above is the guard; this bound only catches a quadratic
	// plan, and stays far above race-detector cost on a loaded host.
	assert.Less(t, time.Since(start), 120*time.Second)
}

// A lookup error other than "no such node" reaches the caller instead of
// being replaced by the raw key.
func TestHistorySize_NodeLookupError_IsReturned(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	_, err = s.writeDB.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field)
		VALUES ('E1', 'k1', '2026-01-01T00:00:00.000000000Z', 'a', 'local', 'change', 'status')`)
	require.NoError(t, err)
	_, err = s.writeDB.Exec(`ALTER TABLE nodes RENAME COLUMN uid TO uid_renamed`)
	require.NoError(t, err)
	_, err = s.HistorySize(context.Background(), 5, 0)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "uid")
}
