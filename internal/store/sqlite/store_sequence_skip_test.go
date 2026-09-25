// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
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
// number, which is handed out as is. The child case holds ids next to
// AA-1's namespace that are not in it: the root AA-123, whose id extends
// AA-1 with digits, and AA-1-9, a root of the prefix AA-1.
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
			[]seqEntry{{"AA-1", 1}, {"AA-1.1", 1}, {"AA-1.2", 2}, {"AA-2", 2}, {"AA-2.8", 8},
				{"AA-123", 123}, {"AA-1-9", 9}}, nil, "AA:AA-1", []int{3, 4}},
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
// counter at maxSequence, or past it (a counter can be past the limit after
// an import of a store that holds such a task, MTIX-107.56), has no number
// left. NextSequence fails with an
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
	require.Equal(t, 2147483647, counterOf(t, s, "AA:"), "the skip writes nothing past the limit")
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

// TestNextSequence_CounterMovedOnBeforeSkip_NeverLowered: another
// allocation moves the counter from 1 to 9 after NextSequence took 1 and
// before the skip runs (a test trigger stands in for it). The taken
// number's namespace holds only 1 and 2, so the skip hands out one more
// than the counter, 10, and never lowers the counter to 3, below a number
// already handed out. Root and child keys.
func TestNextSequence_CounterMovedOnBeforeSkip_NeverLowered(t *testing.T) {
	tests := []struct {
		key   string
		nodes []seqEntry
	}{
		{"AA:", []seqEntry{{"AA-1", 1}, {"AA-2", 2}}},
		{"AA:AA-1", []seqEntry{{"AA-1", 1}, {"AA-1.1", 1}, {"AA-1.2", 2}}},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			for _, n := range tt.nodes {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)))
			}
			setCounterOf(t, s, tt.key, 0)
			_, err := s.WriteDB().ExecContext(ctx, `CREATE TRIGGER test_concurrent_allocation
				AFTER UPDATE OF value ON sequences WHEN OLD.value = 0 AND NEW.value = 1
				BEGIN UPDATE sequences SET value = 9 WHERE key = NEW.key; END`)
			require.NoError(t, err)

			got, err := s.NextSequence(ctx, tt.key)

			require.NoError(t, err)
			require.Equal(t, 10, got)
			require.Equal(t, 10, counterOf(t, s, tt.key), "the counter never drops below 9")
		})
	}
}

// TestNextSequence_ChildIDOfAnotherParentRow_Counted: an id in the
// namespace '<parent>.<digits>' holds that number whatever its parent_id
// says (a pulled create can carry a parent_id that disagrees with its id),
// so the skip counts it and the create does not fail on it. The second
// case is the round-2 reviewer's probe: the counter at 4 hands out 5,
// which AA-1.5 holds, and the skip hands out 6.
func TestNextSequence_ChildIDOfAnotherParentRow_Counted(t *testing.T) {
	tests := []struct {
		name    string
		counter int
		under   []seqEntry // children whose parent_id is AA-1
		other   []seqEntry // ids under AA-1 whose parent_id is empty
		want    int
	}{
		{"counter lost", 0, []seqEntry{{"AA-1.1", 1}}, []seqEntry{{"AA-1.2", 2}}, 3},
		{"counter at 4", 4, []seqEntry{{"AA-1.1", 1}, {"AA-1.2", 2}}, []seqEntry{{"AA-1.5", 5}}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			require.NoError(t, s.CreateNode(ctx, seqNode("AA-1", 1)))
			for _, n := range tt.under {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)))
			}
			for _, n := range tt.other {
				odd := seqNode(n.id, n.seq)
				odd.ParentID = ""
				require.NoError(t, s.CreateNode(ctx, odd))
			}
			setCounterOf(t, s, "AA:AA-1", tt.counter)

			got, err := s.NextSequence(ctx, "AA:AA-1")

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.want, counterOf(t, s, "AA:AA-1"))
		})
	}
}

// TestNextSequence_RootNamespace_ExactPrefixOnly: the root namespace of a
// key is matched exactly and case-sensitively: a lowercase aa-50 is not in
// AA's namespace, and the '_' of A_B is not a wildcard, so AXB-9 is not in
// A_B's.
func TestNextSequence_RootNamespace_ExactPrefixOnly(t *testing.T) {
	tests := []struct {
		name  string
		key   string
		nodes []seqEntry
	}{
		{"lowercase id", "AA:", []seqEntry{{"AA-1", 1}, {"AA-50", 50}}},
		{"underscore in the prefix", "A_B:", []seqEntry{{"A_B-1", 1}, {"AXB-9", 9}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			for _, n := range tt.nodes {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)), n.id)
			}
			lowerCaseID(t, s, "AA-50")

			got, err := s.NextSequence(ctx, tt.key)

			require.NoError(t, err)
			require.Equal(t, 2, got)
		})
	}
}

// lowerCaseID rewrites the id of the node id, if the store holds it, to
// lower case: mtix never creates such an id, but a store can hold one.
func lowerCaseID(t *testing.T, s *sqlite.Store, id string) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET id = lower(id) WHERE id = ?`, id)
	require.NoError(t, err)
}

// TestNextSequence_CounterRowDeletedBeforeSkip_NoFalseLimitError: the
// counter row can disappear between the allocation and the skip (an
// import's rebuild deletes every counter before it writes them again,
// MTIX-107.56; a test trigger stands in for it). The skip then creates the
// counter past the highest number instead of failing with the limit
// error, which it returns only when that number is at the limit.
func TestNextSequence_CounterRowDeletedBeforeSkip_NoFalseLimitError(t *testing.T) {
	tests := []struct {
		name      string
		key       string
		nodes     []seqEntry
		seed      int
		want      int
		wantLimit bool
	}{
		{"root key", "AA:", []seqEntry{{"AA-1", 1}, {"AA-2", 2}}, 0, 3, false},
		{"child key", "AA:AA-1", []seqEntry{{"AA-1", 1}, {"AA-1.1", 1}, {"AA-1.2", 2}}, 0, 3, false},
		{"highest number at the limit", "AA:", []seqEntry{{"AA-2147483647", 2147483647}}, 2147483646, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			for _, n := range tt.nodes {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)))
			}
			setCounterOf(t, s, tt.key, tt.seed)
			// The allocation increments the counter by one; the trigger then
			// deletes the row, before the skip runs.
			_, err := s.WriteDB().ExecContext(ctx, `CREATE TRIGGER test_counter_row_deleted
				AFTER UPDATE OF value ON sequences WHEN NEW.value = OLD.value + 1
				BEGIN DELETE FROM sequences WHERE key = NEW.key; END`)
			require.NoError(t, err)

			got, err := s.NextSequence(ctx, tt.key)

			if tt.wantLimit {
				require.ErrorIs(t, err, model.ErrInvalidInput)
				require.ErrorContains(t, err, "2147483647")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.want, counterOf(t, s, tt.key))
		})
	}
}

// TestNextSequence_NonASCIIParentID_CountsByCharacters: a pulled node id
// can hold a character that is not ASCII (only its project prefix is
// checked), and SQLite's SUBSTR counts characters, not bytes. Under the
// parent AA-1\u00e9, whose children are .1, .2 and .21, the skip hands out
// 22, not the taken 2.
func TestNextSequence_NonASCIIParentID_CountsByCharacters(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	parent := "AA-1\u00e9"
	require.NoError(t, s.CreateNode(ctx, seqNode(parent, 1)))
	for _, n := range []int{1, 2, 21} {
		require.NoError(t, s.CreateNode(ctx, seqNode(fmt.Sprintf("%s.%d", parent, n), n)))
	}

	got, err := s.NextSequence(ctx, "AA:"+parent)

	require.NoError(t, err)
	require.Equal(t, 22, got)
}

// TestNextSequence_SkipToLimit_HandsOutLimitThenFails: the skip may hand
// out 2147483647 itself, the highest number mtix numbers tasks with, and
// only the allocation after it fails with the limit error. With the
// counter row present, AA-2147483645 and AA-2147483646 are taken and the
// counter is at 2147483644; with the row deleted between the allocation
// and the skip (a test trigger), AA-2147483646 is taken and the counter
// is at 2147483645.
func TestNextSequence_SkipToLimit_HandsOutLimitThenFails(t *testing.T) {
	tests := []struct {
		name      string
		nodes     []seqEntry
		counter   int
		deleteRow bool
	}{
		{"counter row present", []seqEntry{{"AA-2147483645", 2147483645}, {"AA-2147483646", 2147483646}},
			2147483644, false},
		{"counter row deleted before the skip", []seqEntry{{"AA-2147483646", 2147483646}}, 2147483645, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			ctx := context.Background()
			for _, n := range tt.nodes {
				require.NoError(t, s.CreateNode(ctx, seqNode(n.id, n.seq)))
			}
			setCounterOf(t, s, "AA:", tt.counter)
			if tt.deleteRow {
				// The allocation increments the counter by one; the trigger
				// then deletes the row, before the skip runs.
				_, err := s.WriteDB().ExecContext(ctx, `CREATE TRIGGER test_counter_row_deleted
					AFTER UPDATE OF value ON sequences WHEN NEW.value = OLD.value + 1
					BEGIN DELETE FROM sequences WHERE key = NEW.key; END`)
				require.NoError(t, err)
			}

			got, err := s.NextSequence(ctx, "AA:")

			require.NoError(t, err)
			require.Equal(t, 2147483647, got)
			require.Equal(t, 2147483647, counterOf(t, s, "AA:"))
			_, err = s.NextSequence(ctx, "AA:")
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.ErrorContains(t, err, "2147483647")
		})
	}
}
