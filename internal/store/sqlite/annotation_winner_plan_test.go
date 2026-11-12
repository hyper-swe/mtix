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

// insertAnnotationComment writes a raw comment event carrying annotation a1.
// A nil uid stores a UID-less (legacy) event.
func insertAnnotationComment(t *testing.T, raw *sql.DB, id, node string, uid any, lamport int64) {
	t.Helper()
	_, err := raw.ExecContext(context.Background(), `INSERT INTO sync_events
 (event_id, project_prefix, node_id, op_type, payload, wall_clock_ts, lamport_clock, vector_clock, author_id, author_machine_hash, created_at, uid)
 VALUES (?, 'MTIX', ?, 'comment', ?, 1, ?, '{}', 'alice', '0123456789abcdef', '2026-01-01T00:00:00Z', ?)`,
		id, node, `{"annotation":{"id":"a1"}}`, lamport, uid)
	require.NoError(t, err)
}

func annotationWinnerFor(t *testing.T, s *Store, e *model.SyncEvent) string {
	t.Helper()
	var got string
	err := s.WithTx(context.Background(), func(tx *sql.Tx) error {
		var err error
		got, err = annotationSnapshotWinner(context.Background(), tx, e, "a1")
		return err
	})
	require.NoError(t, err)
	return got
}

// The UID form matches the event's uid on any node_id (a renumbered node keeps
// its uid) and falls back to the display number only for UID-less events;
// other nodes' comments win only when they carry the same uid.
func TestAnnotationSnapshotWinner_UIDForm_FallsBackToNodeForUIDLess(t *testing.T) {
	s, raw := applyTestStore(t)
	insertAnnotationComment(t, raw, "with-uid", "MTIX-1", "uid-1", 5)
	insertAnnotationComment(t, raw, "uid-less", "MTIX-1", nil, 7)
	for i := 0; i < 20; i++ {
		node := fmt.Sprintf("MTIX-%d", 100+i)
		insertAnnotationComment(t, raw, fmt.Sprintf("noise-uid-%02d", i), node, fmt.Sprintf("uid-other-%d", i), int64(50+i))
		insertAnnotationComment(t, raw, fmt.Sprintf("noise-legacy-%02d", i), node, nil, int64(80+i))
	}
	e := &model.SyncEvent{NodeID: "MTIX-1", UID: "uid-1"}
	require.Equal(t, "uid-less", annotationWinnerFor(t, s, e))

	insertAnnotationComment(t, raw, "with-uid-high", "MTIX-1", "uid-1", 9)
	require.Equal(t, "with-uid-high", annotationWinnerFor(t, s, e))

	// Same-uid event under another display number (renumbered node) wins on
	// the highest lamport, beating all other-node noise.
	insertAnnotationComment(t, raw, "same-uid-renumbered", "MTIX-200", "uid-1", 200)
	require.Equal(t, "same-uid-renumbered", annotationWinnerFor(t, s, e))
}

// On a lamport tie the higher event_id wins, in both query forms and whatever
// the insertion order.
func TestAnnotationSnapshotWinner_LamportTie_HigherEventIDWins(t *testing.T) {
	forms := []struct {
		name string
		uid  any
		e    *model.SyncEvent
	}{
		{"no uid", nil, &model.SyncEvent{NodeID: "MTIX-1"}},
		{"uid", "uid-1", &model.SyncEvent{NodeID: "MTIX-1", UID: "uid-1"}},
	}
	orders := [][]string{{"tie-a", "tie-b"}, {"tie-b", "tie-a"}}
	for _, f := range forms {
		for _, order := range orders {
			t.Run(f.name+"/"+strings.Join(order, ","), func(t *testing.T) {
				s, raw := applyTestStore(t)
				for _, id := range order {
					insertAnnotationComment(t, raw, id, "MTIX-1", f.uid, 7)
				}
				require.Equal(t, "tie-b", annotationWinnerFor(t, s, f.e))
			})
		}
	}
}
