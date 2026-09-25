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

// TestRenumberSubtree_RootNode_CountersCoverRootsAndSubtree: renumbering
// the root RNB-1 to 5 raises the root counter of RNB to the highest root
// number (8, the untouched root RNB-8), and the counter of every parent in
// the moved subtree, now under RNB-5, to the highest number under it. A
// root counter already above (20) keeps its value.
func TestRenumberSubtree_RootNode_CountersCoverRootsAndSubtree(t *testing.T) {
	tests := []struct {
		name string
		seed map[string]int
		want map[string]int
	}{
		{"counters behind", nil, map[string]int{
			"RNB:": 8, "RNB:RNB-5": 7, "RNB:RNB-5.4": 2, "RNB:RNB-5.4.1": 1, "RNB:RNB-5.4.1.1": 1,
		}},
		{"root counter ahead is kept", map[string]int{"RNB:": 20}, map[string]int{
			"RNB:": 20, "RNB:RNB-5": 7,
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			seedRenumberTree(t, s)
			require.NoError(t, s.CreateNode(ctx, seqNode("RNB-8", 8)))
			for key, v := range tt.seed {
				setCounterOf(t, s, key, v)
			}

			require.NoError(t, s.RenumberSubtree(ctx, "RNB-1", 5))

			for key, want := range tt.want {
				require.Equal(t, want, counterOf(t, s, key), key)
			}
		})
	}
}

// TestRenumberSubtree_NeighbourIDs_NotInParentNamespace: the ids next to
// P-1's child namespace are not in it: the root P-123, whose id extends
// P-1 with digits, and P-1-9, a root of the prefix P-1. Renumbering P-1.4
// to 2 leaves P:P-1 at 2.
func TestRenumberSubtree_NeighbourIDs_NotInParentNamespace(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, n := range []seqEntry{{"P-1", 1}, {"P-1.1", 1}, {"P-1.4", 4}, {"P-123", 123}, {"P-1-9", 9}} {
		require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)), n.id)
	}

	require.NoError(t, s.RenumberSubtree(ctx, "P-1.4", 2))

	require.Equal(t, 2, counterOf(t, s, "P:P-1"))
}

// TestRenumberSubtree_UncountedIDs_CountersIgnoreThem: a number above
// maxSequence (2147483647), and the id of another prefix that starts the
// same way (RNB-9-3, of prefix RNB-9), never raise a counter. Renumbering
// RNB-1.4, which has a child RNB-1.4.9999999999, to 5 leaves RNB:RNB-1.5 at
// 2; renumbering the root RNB-1 to 5 leaves RNB: at 5.
func TestRenumberSubtree_UncountedIDs_CountersIgnoreThem(t *testing.T) {
	tests := []struct {
		name  string
		extra []seqEntry
		id    string
		key   string
		want  int
	}{
		{"child above the limit", []seqEntry{{"RNB-1.4.9999999999", 9999999999}}, "RNB-1.4", "RNB:RNB-1.5", 2},
		{"root above the limit, and another prefix", []seqEntry{{"RNB-9999999999", 9999999999}, {"RNB-9-3", 3}},
			"RNB-1", "RNB:", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			seedRenumberTree(t, s)
			for _, n := range tt.extra {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)), n.id)
			}

			require.NoError(t, s.RenumberSubtree(ctx, tt.id, 5))

			require.Equal(t, tt.want, counterOf(t, s, tt.key))
		})
	}
}

// TestRenumberSubtree_RootNamespace_ExactPrefixOnly: the root counter a
// root renumber raises counts only the ids of its own prefix, matched
// exactly and case-sensitively: not a lowercase rnb-50, and not AXB-9 for
// the prefix A_B, whose '_' is not a wildcard.
func TestRenumberSubtree_RootNamespace_ExactPrefixOnly(t *testing.T) {
	tests := []struct {
		name  string
		nodes []seqEntry
		id    string
		key   string
		want  int
	}{
		{"lowercase id", []seqEntry{{"RNB-1", 1}, {"RNB-50", 50}}, "RNB-1", "RNB:", 5},
		{"underscore in the prefix", []seqEntry{{"A_B-1", 1}, {"AXB-9", 9}}, "A_B-1", "A_B:", 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			for _, n := range tt.nodes {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)), n.id)
			}
			lowerCaseID(t, s, "RNB-50")

			require.NoError(t, s.RenumberSubtree(ctx, tt.id, 5))

			require.Equal(t, tt.want, counterOf(t, s, tt.key))
		})
	}
}

// TestRenumberSubtree_NonASCIIRootPrefix_CountsByCharacters: the root
// raise finds the numbers after '<prefix>-' by characters, as SQLite's
// SUBSTR counts them. mtix never creates a prefix that is not ASCII, but a
// store can hold one, written directly; renumbering its root \u00c9A-1 to 5
// next to \u00c9A-12 raises the root counter to 12.
func TestRenumberSubtree_NonASCIIRootPrefix_CountsByCharacters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, n := range []seqEntry{{"EA-1", 1}, {"EA-12", 12}} {
		require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)))
		// Test seam: the prefix written directly, past the prefix grammar.
		_, err := s.WriteDB().ExecContext(ctx,
			`UPDATE nodes SET id = ? || SUBSTR(id, 2), project = ? WHERE id = ?`, "\u00c9", "\u00c9A", n.id)
		require.NoError(t, err)
	}

	require.NoError(t, s.RenumberSubtree(ctx, "\u00c9A-1", 5))

	require.Equal(t, 12, counterOf(t, s, "\u00c9A:"))
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
