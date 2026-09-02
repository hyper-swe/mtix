// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-88: backfill walked `nodes` in created_at order, but created_at is
// wall-clock and says nothing about topology. Reparenting and renumbering
// leave a child row whose created_at predates the parent it now hangs
// under. Because lamport is assigned monotonically in walk order, such a
// child was emitted with a LOWER lamport than its parent — and
// nodes.parent_id is a FOREIGN KEY, so a consumer applying in lamport
// order (mtix sync clone) failed the child insert with "FOREIGN KEY
// constraint failed", deterministically.
//
// Reported against 0.5.1-beta with 8 violations across two subtrees.

// seedBackfillNode inserts one node with an explicit creation time.
func seedBackfillNode(t *testing.T, s *sqlite.Store, id, parentID string, depth, seq int, created time.Time) {
	t.Helper()
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID:        id,
		ParentID:  parentID,
		Project:   "TEST",
		Title:     id,
		Status:    model.StatusOpen,
		NodeType:  model.NodeTypeAuto,
		Priority:  model.PriorityMedium,
		Weight:    1.0,
		Creator:   "test",
		CreatedAt: created,
		UpdatedAt: created,
		Depth:     depth,
		Seq:       seq,
	}))
}

// backfillLamportByNode returns the lamport of each node's create_node
// event, and the parent_id of every live node.
func backfillLamportByNode(t *testing.T, s *sqlite.Store) (map[string]int64, map[string]string) {
	t.Helper()
	ctx := context.Background()

	lamport := map[string]int64{}
	rows, err := s.WriteDB().QueryContext(ctx,
		`SELECT node_id, lamport_clock FROM sync_events
		  WHERE op_type = 'create_node' ORDER BY lamport_clock ASC`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var lam int64
		require.NoError(t, rows.Scan(&id, &lam))
		lamport[id] = lam
	}
	require.NoError(t, rows.Err())

	parent := map[string]string{}
	prows, err := s.WriteDB().QueryContext(ctx,
		`SELECT id, COALESCE(parent_id, '') FROM nodes WHERE deleted_at IS NULL`)
	require.NoError(t, err)
	defer func() { _ = prows.Close() }()
	for prows.Next() {
		var id, pid string
		require.NoError(t, prows.Scan(&id, &pid))
		parent[id] = pid
	}
	require.NoError(t, prows.Err())

	return lamport, parent
}

// TestBackfill_EmitsParentBeforeReparentedChild pins the MTIX-88 fix: a
// child whose created_at predates its current parent must still be
// emitted after that parent.
func TestBackfill_EmitsParentBeforeReparentedChild(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)

	// The child row is created FIRST — as it would have been before a
	// reparent moved it under a parent that did not yet exist.
	seedBackfillNode(t, s, "TEST-1", "", 0, 1, base)
	seedBackfillNode(t, s, "TEST-2", "", 0, 2, base.Add(time.Minute))

	// Reparent TEST-1 under the younger TEST-2, exactly as the reconcile
	// path's assignParentToFormerRoots does.
	_, err := s.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET parent_id = 'TEST-2', depth = 1 WHERE id = 'TEST-1'`)
	require.NoError(t, err)

	_, err = s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)

	_, err = s.Backfill(ctx, false)
	require.NoError(t, err)

	lamport, parent := backfillLamportByNode(t, s)
	require.Contains(t, lamport, "TEST-1")
	require.Contains(t, lamport, "TEST-2")
	require.Less(t, lamport["TEST-2"], lamport["TEST-1"],
		"parent TEST-2 must be emitted before its reparented child TEST-1; "+
			"created_at order alone puts the child first and breaks the parent_id FK on apply")

	for child, p := range parent {
		if p == "" {
			continue
		}
		require.Lessf(t, lamport[p], lamport[child],
			"child %s (lamport %d) must not precede parent %s (lamport %d)",
			child, lamport[child], p, lamport[p])
	}
}

// TestBackfill_TopologicalAcrossDeepSubtree covers the reported shape: a
// multi-level subtree whose rows were created in an order unrelated to
// the final hierarchy.
func TestBackfill_TopologicalAcrossDeepSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)

	// Insert flat and oldest-first, then wire up a hierarchy that
	// inverts that order at every level.
	seedBackfillNode(t, s, "TEST-3", "", 0, 3, base)                    // becomes a grandchild
	seedBackfillNode(t, s, "TEST-2", "", 0, 2, base.Add(time.Minute))   // becomes a child
	seedBackfillNode(t, s, "TEST-1", "", 0, 1, base.Add(2*time.Minute)) // becomes the root
	seedBackfillNode(t, s, "TEST-4", "", 0, 4, base.Add(3*time.Minute)) // unrelated root

	for _, q := range []string{
		`UPDATE nodes SET parent_id = 'TEST-1', depth = 1 WHERE id = 'TEST-2'`,
		`UPDATE nodes SET parent_id = 'TEST-2', depth = 2 WHERE id = 'TEST-3'`,
	} {
		_, err := s.WriteDB().ExecContext(ctx, q)
		require.NoError(t, err)
	}

	_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)

	_, err = s.Backfill(ctx, false)
	require.NoError(t, err)

	lamport, parent := backfillLamportByNode(t, s)
	require.Len(t, lamport, 4, "every live node must get a create_node event")

	for child, p := range parent {
		if p == "" {
			continue
		}
		require.Lessf(t, lamport[p], lamport[child],
			"child %s (lamport %d) must not precede parent %s (lamport %d)",
			child, lamport[child], p, lamport[p])
	}
}

// TestBackfill_PreservesCreatedAtOrderWhenAlreadyTopological guards
// against the reordering churning an event stream that was already
// correct: siblings and unrelated roots must keep created_at order.
func TestBackfill_PreservesCreatedAtOrderWhenAlreadyTopological(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	base := time.Now().UTC().Add(-time.Hour)
	seedBackfillNode(t, s, "TEST-1", "", 0, 1, base)
	seedBackfillNode(t, s, "TEST-2", "TEST-1", 1, 2, base.Add(time.Minute))
	seedBackfillNode(t, s, "TEST-3", "TEST-1", 1, 3, base.Add(2*time.Minute))
	seedBackfillNode(t, s, "TEST-4", "", 0, 4, base.Add(3*time.Minute))

	_, err := s.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)

	_, err = s.Backfill(ctx, false)
	require.NoError(t, err)

	lamport, _ := backfillLamportByNode(t, s)
	require.Less(t, lamport["TEST-1"], lamport["TEST-2"])
	require.Less(t, lamport["TEST-2"], lamport["TEST-3"], "siblings keep created_at order")
	require.Less(t, lamport["TEST-3"], lamport["TEST-4"], "unrelated roots keep created_at order")
}
