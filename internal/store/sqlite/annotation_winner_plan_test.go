// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// annotationWinnerPlan returns the EXPLAIN QUERY PLAN detail text for one of the
// package's own constant statements. CLAUDE.md SQL Rule 1b: test file, exact
// prefix, constant SQL, bound arguments still passed as parameters.
func annotationWinnerPlan(t *testing.T, query string, args ...any) string {
	t.Helper()
	_, raw := applyTestStore(t)
	rows, err := raw.QueryContext(context.Background(), "EXPLAIN QUERY PLAN "+query, args...)
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

func TestAnnotationWinnerQueries_Plan_AvoidsOpTypeIndex(t *testing.T) {
	tests := []struct {
		name  string
		query string
		args  []any
		want  []string
	}{
		{"no uid", annotationWinnerNoUIDSQL, []any{"MTIX-1", "a1"}, []string{"idx_sync_events_node"}},
		{"uid", annotationWinnerUIDSQL, []any{"uid-1", "MTIX-1", "a1"}, []string{"idx_sync_events_uid", "idx_sync_events_node"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := annotationWinnerPlan(t, tt.query, tt.args...)
			require.NotContains(t, plan, "idx_sync_events_op_type", plan)
			found := false
			for _, w := range tt.want {
				found = found || strings.Contains(plan, w)
			}
			require.True(t, found, "plan must use %v: %s", tt.want, plan)
		})
	}
}

func TestAnnotationSnapshotWinner_ManyOtherNodeComments_SameWinner(t *testing.T) {
	s, raw := applyTestStore(t)
	ctx := context.Background()
	require.NoError(t, applyOnce(t, s, makeApplyEvent(t, model.OpCreateNode, "MTIX-1", "alice", 1,
		&model.CreateNodePayload{Title: "x"})))
	for i := 0; i < 300; i++ {
		node := fmt.Sprintf("MTIX-%d", 100+i)
		_, err := raw.ExecContext(ctx, `INSERT INTO sync_events
 (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash, created_at)
 VALUES (?, 'MTIX', ?, 'comment', ?, 1, ?, '{}', 'bob', '0123456789abcdef', '2026-01-01T00:00:00Z')`,
			fmt.Sprintf("noise-%04d", i), node, `{"annotation":{"id":"a1"}}`, int64(10+i))
		require.NoError(t, err)
	}
	insert := func(id string, lamport int64) {
		_, err := raw.ExecContext(ctx, `INSERT INTO sync_events
 (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash, created_at)
 VALUES (?, 'MTIX', 'MTIX-1', 'comment', ?, 1, ?, '{}', 'alice', '0123456789abcdef', '2026-01-01T00:00:00Z')`,
			id, `{"annotation":{"id":"a1"}}`, lamport)
		require.NoError(t, err)
	}
	insert("own-low", 5)
	insert("own-high", 7)

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		got, err := annotationSnapshotWinner(ctx, tx, &model.SyncEvent{NodeID: "MTIX-1"}, "a1")
		require.NoError(t, err)
		require.Equal(t, "own-high", got)
		return nil
	})
	require.NoError(t, err)
}
