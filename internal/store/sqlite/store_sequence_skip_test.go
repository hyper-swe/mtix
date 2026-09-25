// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// NextSequence hands out the next free number of its key (MTIX-95.38): when
// the number the counter reaches is already held by a node, live or
// soft-deleted, it moves the counter past the highest number under that
// parent, once, and hands that number out. The nodes below are inserted
// with explicit numbers, which leaves their counters behind, as an
// interrupted import or a pull before MTIX-95.38 does.

// seqEntry is a node id and its number.
type seqEntry struct {
	id  string
	seq int
}

// TestNextSequence_CounterBehind_SkipsPastHighestInItsNamespace covers the
// root and child namespaces, numbers held by soft-deleted nodes, and a free
// number, which is handed out as is.
func TestNextSequence_CounterBehind_SkipsPastHighestInItsNamespace(t *testing.T) {
	tests := []struct {
		name    string
		nodes   []seqEntry
		deleted []string
		key     string
		want    []int // successive NextSequence results
	}{
		{"root taken, another project's higher roots ignored",
			[]seqEntry{{"AA-1", 1}, {"AA-2", 2}, {"AA-3", 3}, {"BB-9", 9}}, nil, "AA:", []int{4, 5}},
		{"child taken, other parents' children ignored",
			[]seqEntry{{"AA-1", 1}, {"AA-1.1", 1}, {"AA-1.2", 2}, {"AA-2", 2}, {"AA-2.8", 8}}, nil, "AA:AA-1", []int{3, 4}},
		{"numbers held by soft-deleted nodes count",
			[]seqEntry{{"AA-1", 1}, {"AA-2", 2}, {"AA-3", 3}}, []string{"AA-1", "AA-3"}, "AA:", []int{4}},
		{"free number is handed out as is, a taken one is skipped",
			[]seqEntry{{"AA-2", 2}}, nil, "AA:", []int{1, 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			for _, n := range tt.nodes {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)), n.id)
			}
			for _, id := range tt.deleted {
				require.NoError(t, s.DeleteNode(ctx, id, false, "test"))
			}

			for i, want := range tt.want {
				got, err := s.NextSequence(ctx, tt.key)
				require.NoError(t, err)
				require.Equal(t, want, got, "call %d", i+1)
			}
		})
	}
}

// TestNextSequence_SkipWriteFails_ReturnsError: when the counter cannot be
// moved past the taken numbers, NextSequence fails instead of handing out a
// taken number.
func TestNextSequence_SkipWriteFails_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, n := range []seqEntry{{"AA-1", 1}, {"AA-2", 2}, {"AA-3", 3}} {
		require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)))
	}
	_, err := s.WriteDB().ExecContext(ctx, `CREATE TRIGGER test_refuse_skip BEFORE UPDATE ON sequences
		WHEN NEW.value > OLD.value + 1 BEGIN SELECT RAISE(ABORT, 'skip refused'); END`)
	require.NoError(t, err)

	_, err = s.NextSequence(ctx, "AA:")

	require.ErrorContains(t, err, "skip refused")
}
