// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// A renumber moves a node and its subtree to new ids; the numbers under
// the moved node's parent and under every moved parent must stay covered
// by their sequence counters (MTIX-95.38). Before, a renumber wrote no
// counter: the moved node's new id had none, so the next create under it
// picked 1, the number its moved first child holds.

// counterOf returns the value of the sequence counter key on s, 0 when the
// key has no row.
func counterOf(t *testing.T, s *sqlite.Store, key string) int {
	t.Helper()
	var v int
	err := s.ReadDB().QueryRowContext(context.Background(),
		`SELECT value FROM sequences WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return 0
	}
	require.NoError(t, err)
	return v
}

// setCounterOf sets the sequence counter key on s to value.
func setCounterOf(t *testing.T, s *sqlite.Store, key string, value int) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(context.Background(),
		`INSERT INTO sequences (key, value) VALUES (?, ?)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	require.NoError(t, err)
}

// seqNode builds the node id, with the number seq; its parent and project
// follow from the id.
func seqNode(id string, seq int) *model.Node {
	now := time.Date(2026, 9, 25, 9, 0, 0, 0, time.UTC)
	depth := model.ParseIDDepth(id)
	n := &model.Node{
		ID: id, ParentID: model.ParseIDParent(id), Project: model.ParseIDProject(id), Depth: depth, Seq: seq,
		Title: "T " + id, NodeType: model.NodeTypeForDepth(depth), Priority: model.PriorityMedium,
		Status: model.StatusOpen, Weight: 1.0, Creator: "test", CreatedAt: now, UpdatedAt: now,
	}
	n.ContentHash = n.ComputeHash()
	return n
}

// TestRenumberSubtree_MovedSubtree_CountersCoverEveryNumber: renumbering
// RNB-1.4 to 5 moves its subtree to RNB-1.5.*. In its transaction the
// renumber raises the counter of the moved node's parent to the highest
// number its children hold (7, the untouched sibling RNB-1.7), and the
// counter of every parent in the moved subtree to the highest number under
// it. A counter already above keeps its value.
func TestRenumberSubtree_MovedSubtree_CountersCoverEveryNumber(t *testing.T) {
	tests := []struct {
		name string
		seed map[string]int
		want map[string]int
	}{
		{"counters behind", nil, map[string]int{
			"RNB:RNB-1": 7, "RNB:RNB-1.5": 2, "RNB:RNB-1.5.1": 1, "RNB:RNB-1.5.1.1": 1,
		}},
		{"counters ahead are kept", map[string]int{"RNB:RNB-1": 20, "RNB:RNB-1.5": 9}, map[string]int{
			"RNB:RNB-1": 20, "RNB:RNB-1.5": 9, "RNB:RNB-1.5.1": 1, "RNB:RNB-1.5.1.1": 1,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			seedRenumberTree(t, s)
			for key, v := range tt.seed {
				setCounterOf(t, s, key, v)
			}

			require.NoError(t, s.RenumberSubtree(context.Background(), "RNB-1.4", 5))

			for key, v := range tt.want {
				require.Equal(t, v, counterOf(t, s, key), key)
			}
		})
	}
}

// TestRenumberForHubRejection_MovedSubtree_NextChildContinues: the hub
// rejects PRJX-1.1, which has a child PRJX-1.1.1; the drain renumbers it to
// PRJX-1.2. The counter of PRJX-1.2 covers the moved child, so the next
// child claimed under PRJX-1.2 is 2.
func TestRenumberForHubRejection_MovedSubtree_NextChildContinues(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("PRJX-1", "", "PRJX", "parent")))
	childID := createChild(t, s, "PRJX-1", "PRJX", "child") // PRJX-1.1
	seq, err := s.ClaimNextSeq(ctx, "PRJX", childID)
	require.NoError(t, err)
	require.NoError(t, s.CreateNode(ctx, seqNode(model.BuildID("PRJX", childID, seq), seq)))
	child, err := s.GetNode(ctx, childID)
	require.NoError(t, err)

	newID, err := s.RenumberForHubRejection(ctx, child.UID)

	require.NoError(t, err)
	require.Equal(t, "PRJX-1.2", newID)
	require.Equal(t, 1, counterOf(t, s, "PRJX:PRJX-1.2"), "the moved child's number is covered")
	require.Equal(t, 2, counterOf(t, s, "PRJX:PRJX-1"))
	next, err := s.ClaimNextSeq(ctx, "PRJX", "PRJX-1.2")
	require.NoError(t, err)
	require.Equal(t, 2, next)
}

// TestRenumberSubtree_CounterWriteFails_NothingMoves: when a counter cannot
// be written, the renumber fails and its transaction moves nothing.
func TestRenumberSubtree_CounterWriteFails_NothingMoves(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	seedRenumberTree(t, s)
	_, err := s.WriteDB().ExecContext(ctx, `CREATE TRIGGER test_refuse_sequence BEFORE INSERT ON sequences
		BEGIN SELECT RAISE(ABORT, 'sequence write refused'); END`)
	require.NoError(t, err)

	err = s.RenumberSubtree(ctx, "RNB-1.4", 5)

	require.ErrorContains(t, err, "sequence write refused")
	_, err = s.GetNode(ctx, "RNB-1.4.1")
	require.NoError(t, err, "the subtree stays where it was")
	_, err = s.GetNode(ctx, "RNB-1.5")
	require.ErrorIs(t, err, model.ErrNotFound)
}
