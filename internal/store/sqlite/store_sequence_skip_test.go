// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
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

// staleEntry is a node id, the number its seq column records, and the
// project its project column records, which may disagree with the id, as
// they do after mtix sync reconcile --rename-to or --import-as.
type staleEntry struct {
	id      string
	seq     int
	project string
}

// TestNextSequence_StaleProjectAndSeqColumns_SkipsByIDs: the skip reads
// the taken numbers from the ids of the namespace, never from the project
// or seq columns. For a root key they are the ids <PROJECT>-<digits>; ids
// of children and of a prefix that only starts the same do not count. For
// a child key they are the digits after "<parent>." in its children's ids.
func TestNextSequence_StaleProjectAndSeqColumns_SkipsByIDs(t *testing.T) {
	tests := []struct {
		name  string
		nodes []staleEntry
		key   string
		want  int
	}{
		{"root namespace", []staleEntry{
			{"AA-1", 0, "ZZ"}, {"AA-2", 0, "ZZ"}, {"AA-B-9", 9, "AA-B"}, {"AA-9-5", 5, "AA-9"}, {"AA-1.7", 7, "ZZ"},
		}, "AA:", 3},
		{"child namespace", []staleEntry{
			{"AA-1", 1, "AA"}, {"AA-1.1", 40, "ZZ"}, {"AA-1.2", 50, "ZZ"}, {"AA-1.2.9", 9, "ZZ"},
		}, "AA:AA-1", 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			for _, n := range tt.nodes {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)), n.id)
				_, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET project = ? WHERE id = ?`, n.project, n.id)
				require.NoError(t, err)
			}

			got, err := s.NextSequence(ctx, tt.key)

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

// TestNextSequence_NumberAboveBound_IgnoredBySkip: an id numbered above
// maxSequence (2147483647) never comes from a counter, so the skip does
// not count it: AA-1 is taken, AA-9999999999 is ignored, and the next
// number is 2.
func TestNextSequence_NumberAboveBound_IgnoredBySkip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, n := range []seqEntry{{"AA-1", 1}, {"AA-9999999999", 9999999999}} {
		require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)))
	}

	got, err := s.NextSequence(ctx, "AA:")

	require.NoError(t, err)
	require.Equal(t, 2, got)
}

// TestNextSequence_CounterAtOrPastLimit_FailsClearlyCounterUnchanged: a
// counter at maxSequence, or past it (as a counter set by a pulled task
// before MTIX-95.38 can be), has no number left. NextSequence fails with an
// error naming the limit and leaves the counter as it was, an integer,
// instead of overflowing it to a real number.
func TestNextSequence_CounterAtOrPastLimit_FailsClearlyCounterUnchanged(t *testing.T) {
	for _, counter := range []int64{2147483647, 9223372036854775807} {
		t.Run(fmt.Sprintf("counter %d", counter), func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			_, err := s.WriteDB().ExecContext(ctx,
				`INSERT INTO sequences (key, value) VALUES ('AA:', ?)`, counter)
			require.NoError(t, err)

			_, err = s.NextSequence(ctx, "AA:")

			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorContains(t, err, "2147483647")
			var kind string
			var value int64
			require.NoError(t, s.ReadDB().QueryRowContext(ctx,
				`SELECT typeof(value), value FROM sequences WHERE key = 'AA:'`).Scan(&kind, &value))
			require.Equal(t, "integer", kind)
			require.Equal(t, counter, value)
		})
	}
}

// TestNextSequence_SkipPastLimit_FailsClearly: the counter hands out
// 2147483647, which AA-2147483647 holds; the skip would need 2147483648,
// above maxSequence, so NextSequence fails with an error naming the limit
// instead of handing it out.
func TestNextSequence_SkipPastLimit_FailsClearly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, seqNode("AA-2147483647", 2147483647)))
	setCounterOf(t, s, "AA:", 2147483646)

	_, err := s.NextSequence(ctx, "AA:")

	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.ErrorContains(t, err, "2147483647")
}

// TestNextSequence_ChildOutsideParentNamespace_NotCounted: a child row
// whose parent_id names AA-3 but whose id is not '<AA-3>.<digits>' (AA-5.9,
// or the grandchild-shaped AA-3.7.2), as a malformed pulled create can
// leave, cannot hold a number of AA-3's namespace, so the skip does not
// count it: the next number after AA-3.1 is 2, not 10 or 8.
func TestNextSequence_ChildOutsideParentNamespace_NotCounted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, n := range []seqEntry{{"AA-3", 3}, {"AA-3.1", 1}} {
		require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)))
	}
	for _, n := range []seqEntry{{"AA-5.9", 9}, {"AA-3.7.2", 2}} {
		odd := seqNode(n.id, n.seq)
		odd.ParentID = "AA-3"
		require.NoError(t, s.CreateNode(ctx, odd), n.id)
	}

	got, err := s.NextSequence(ctx, "AA:AA-3")

	require.NoError(t, err)
	require.Equal(t, 2, got)
}
