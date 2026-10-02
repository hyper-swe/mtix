// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// manyAuthors builds a clock of n authors a000.. with counter i+1.
func manyAuthors(n int) VectorClock {
	vc := make(VectorClock, n)
	for i := 0; i < n; i++ {
		vc[fmt.Sprintf("a%03d", i)] = int64(i + 1)
	}
	return vc
}

func TestVectorClockPrune_TableDriven(t *testing.T) {
	tests := []struct {
		name        string
		in          VectorClock
		keep        []string
		wantLen     int
		wantDropped int
		mustKeep    []string
		mustDrop    []string
	}{
		{"within cap unchanged", manyAuthors(100), nil, 100, 0, []string{"a000"}, nil},
		{"101 drops the smallest counter", manyAuthors(101), nil, 100, 1, []string{"a100"}, []string{"a000"}},
		{"local author with smallest counter is kept", manyAuthors(150), []string{"a000"}, 100, 50,
			[]string{"a000", "a149"}, []string{"a001", "a050"}},
		{"keep alone over the cap still bounded", manyAuthors(120), keysOf(manyAuthors(120)), 100, 20,
			[]string{"a000", "a099"}, []string{"a100", "a119"}},
		{"empty", VectorClock{}, nil, 0, 0, nil, nil},
		{"nil", nil, nil, 0, 0, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, dropped := tt.in.Prune(tt.keep...)
			require.Len(t, got, tt.wantLen)
			require.Equal(t, tt.wantDropped, dropped)
			require.NoError(t, got.Validate())
			for _, k := range tt.mustKeep {
				require.Contains(t, got, k)
			}
			for _, k := range tt.mustDrop {
				require.NotContains(t, got, k)
			}
		})
	}
}

func keysOf(vc VectorClock) []string {
	out := make([]string, 0, len(vc))
	for k := range vc {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestVectorClockPrune_TiesBrokenByAuthorID: equal counters drop the larger
// author id first, whatever the map iteration order.
func TestVectorClockPrune_TiesBrokenByAuthorID(t *testing.T) {
	for run := 0; run < 50; run++ {
		vc := make(VectorClock, 130)
		for i := 0; i < 130; i++ {
			vc[fmt.Sprintf("t%03d", i)] = 7
		}
		got, dropped := vc.Prune()
		require.Equal(t, 30, dropped)
		for i := 0; i < 100; i++ {
			require.Contains(t, got, fmt.Sprintf("t%03d", i))
		}
		for i := 100; i < 130; i++ {
			require.NotContains(t, got, fmt.Sprintf("t%03d", i))
		}
	}
}

func TestVectorClockPrune_DeterministicAndNonMutating(t *testing.T) {
	in := manyAuthors(177)
	snapshot := manyAuthors(177)
	first, _ := in.Prune("a005")
	for run := 0; run < 50; run++ {
		again, _ := in.Prune("a005")
		require.Equal(t, first, again)
	}
	require.Equal(t, snapshot, in, "input not mutated")
}

// TestVectorClockPrune_ComparisonSemantics: pruning keeps the surviving
// entries exact, so a clock still dominates its own pruned form's past, and
// pruned authors read as 0 (the documented degradation).
func TestVectorClockPrune_ComparisonSemantics(t *testing.T) {
	older := manyAuthors(150)
	newer := older.Merge(VectorClock{"a149": 500})
	po, _ := older.Prune("a000")
	pn, _ := newer.Prune("a000")

	require.True(t, pn.Dominates(po), "a causally later clock still dominates after both are pruned")
	require.False(t, po.Dominates(pn))
	require.False(t, po.Concurrent(pn))
	for k, v := range po {
		require.Equal(t, older[k], v, "surviving entries are exact")
	}
	// Degradation: an exact comparison orders the two, a pruned one may not
	// (the dropped entry reads as 0), but never inverts the order.
	exact := VectorClock{"x": 1, "y": 2}
	require.True(t, VectorClock{"x": 1, "y": 3}.Dominates(exact))
	require.False(t, exact.Dominates(VectorClock{"x": 1, "y": 3}))
}

func TestVectorClockPrune_ConcurrentUse(t *testing.T) {
	in := manyAuthors(200)
	done := make(chan VectorClock, 8)
	for i := 0; i < 8; i++ {
		go func() { got, _ := in.Prune("a003"); done <- got }()
	}
	first := <-done
	for i := 1; i < 8; i++ {
		require.Equal(t, first, <-done)
	}
}
