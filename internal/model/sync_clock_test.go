// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"errors"
	"math/rand"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestVectorClock_BumpMonotonic(t *testing.T) {
	vc := model.VectorClock{}
	require.Equal(t, int64(1), vc.Bump("alice"))
	require.Equal(t, int64(2), vc.Bump("alice"))
	require.Equal(t, int64(3), vc.Bump("alice"))
	require.Equal(t, int64(1), vc.Bump("bob"), "different author starts at 1")
}

func TestVectorClock_MergeCommutative(t *testing.T) {
	cases := []struct {
		name string
		a, b model.VectorClock
	}{
		{"empty + empty", model.VectorClock{}, model.VectorClock{}},
		{"disjoint", model.VectorClock{"a": 3}, model.VectorClock{"b": 5}},
		{"overlap min/max",
			model.VectorClock{"a": 3, "b": 1},
			model.VectorClock{"a": 1, "b": 5},
		},
		{"identical", model.VectorClock{"a": 7}, model.VectorClock{"a": 7}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ab := tc.a.Merge(tc.b)
			ba := tc.b.Merge(tc.a)
			require.Equal(t, ab, ba, "merge MUST be commutative for FR-18.27 fuzz target")
		})
	}
}

func TestVectorClock_MergeDoesNotMutateInputs(t *testing.T) {
	a := model.VectorClock{"a": 1}
	b := model.VectorClock{"a": 5, "b": 2}
	_ = a.Merge(b)
	require.Equal(t, model.VectorClock{"a": 1}, a, "input a unchanged")
	require.Equal(t, model.VectorClock{"a": 5, "b": 2}, b, "input b unchanged")
}

func TestVectorClock_DominatesAndConcurrent(t *testing.T) {
	cases := []struct {
		name           string
		a, b           model.VectorClock
		aDomB, bDomA   bool
		concurrent, eq bool
	}{
		{"equal", model.VectorClock{"a": 1}, model.VectorClock{"a": 1}, false, false, false, true},
		{"a strictly after b", model.VectorClock{"a": 2}, model.VectorClock{"a": 1}, true, false, false, false},
		{"b strictly after a", model.VectorClock{"a": 1}, model.VectorClock{"a": 2}, false, true, false, false},
		{"concurrent disjoint", model.VectorClock{"a": 1}, model.VectorClock{"b": 1}, false, false, true, false},
		{"concurrent overlap",
			model.VectorClock{"a": 2, "b": 1},
			model.VectorClock{"a": 1, "b": 2},
			false, false, true, false},
		{"empty == empty", model.VectorClock{}, model.VectorClock{}, false, false, false, true},
		{"empty < non-empty", model.VectorClock{}, model.VectorClock{"a": 1}, false, true, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.aDomB, tc.a.Dominates(tc.b))
			require.Equal(t, tc.bDomA, tc.b.Dominates(tc.a))
			require.Equal(t, tc.concurrent, tc.a.Concurrent(tc.b))
			require.Equal(t, tc.concurrent, tc.b.Concurrent(tc.a), "Concurrent symmetric")
			require.Equal(t, tc.eq, tc.a.Equal(tc.b))
		})
	}
}

func TestVectorClock_MarshalDeterministic(t *testing.T) {
	const want = `{"alice":1,"bob":2,"zara":3}`

	// Repeated marshals of the SAME instance must all produce identical
	// bytes. This catches an implementation that toggles sort order
	// between calls (a bug the prior 2-marshal test would miss).
	vc := model.VectorClock{"zara": 3, "alice": 1, "bob": 2}
	for i := 0; i < 16; i++ {
		b, err := json.Marshal(vc)
		require.NoError(t, err)
		require.Equal(t, want, string(b),
			"marshal call %d must produce canonical lexical order", i)
	}

	// Logically equivalent VCs constructed with different key insertion
	// orders must marshal to identical bytes. Go map iteration order is
	// already randomized, but constructing fresh maps with each ordering
	// guarantees we exercise distinct internal hash bucket layouts.
	orderings := [][][2]any{
		{{"alice", int64(1)}, {"bob", int64(2)}, {"zara", int64(3)}},
		{{"zara", int64(3)}, {"bob", int64(2)}, {"alice", int64(1)}},
		{{"bob", int64(2)}, {"alice", int64(1)}, {"zara", int64(3)}},
		{{"zara", int64(3)}, {"alice", int64(1)}, {"bob", int64(2)}},
	}
	for i, ordering := range orderings {
		built := model.VectorClock{}
		for _, kv := range ordering {
			built[kv[0].(string)] = kv[1].(int64)
		}
		b, err := json.Marshal(built)
		require.NoError(t, err)
		require.Equal(t, want, string(b),
			"insertion ordering %d produced non-canonical output", i)
	}
}

func TestVectorClock_MarshalUnmarshalRoundTrip(t *testing.T) {
	vc := model.VectorClock{"alice": 1, "bob": 2, "carol": 3}
	b, err := json.Marshal(vc)
	require.NoError(t, err)

	var got model.VectorClock
	require.NoError(t, json.Unmarshal(b, &got))
	require.Equal(t, vc, got)
}

func TestVectorClock_UnmarshalNullProducesEmpty(t *testing.T) {
	var got model.VectorClock
	require.NoError(t, json.Unmarshal([]byte("null"), &got))
	require.NotNil(t, got)
	require.Empty(t, got)
}

func TestVectorClock_NilMarshalsAsEmptyObject(t *testing.T) {
	var vc model.VectorClock
	b, err := json.Marshal(vc)
	require.NoError(t, err)
	require.Equal(t, "{}", string(b))
}

func TestVectorClock_Validate(t *testing.T) {
	t.Run("happy path", func(t *testing.T) {
		require.NoError(t, model.VectorClock{"a": 1, "b": 100}.Validate())
	})
	t.Run("rejects negative entries", func(t *testing.T) {
		err := model.VectorClock{"a": -1}.Validate()
		require.Error(t, err)
		require.True(t, errors.Is(err, model.ErrInvalidInput))
	})
	t.Run("rejects 2^53 boundary", func(t *testing.T) {
		err := model.VectorClock{"a": int64(1) << 53}.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "2^53")
	})
	t.Run("accepts just below 2^53", func(t *testing.T) {
		require.NoError(t, model.VectorClock{"a": (int64(1) << 53) - 1}.Validate())
	})
	t.Run("rejects > 100 entries", func(t *testing.T) {
		vc := make(model.VectorClock, 101)
		for i := 0; i < 101; i++ {
			vc[stringFromInt(i)] = int64(i)
		}
		err := vc.Validate()
		require.Error(t, err)
		require.Contains(t, err.Error(), "max 100")
	})
}

func TestVectorClock_PropertyMergeAssociative(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	for trial := 0; trial < 100; trial++ {
		a := randomVC(rng, 5)
		b := randomVC(rng, 5)
		c := randomVC(rng, 5)

		left := a.Merge(b).Merge(c)
		right := a.Merge(b.Merge(c))
		require.Equal(t, left, right,
			"merge associativity: trial=%d a=%v b=%v c=%v", trial, a, b, c)
	}
}

func randomVC(rng *rand.Rand, maxKeys int) model.VectorClock {
	vc := model.VectorClock{}
	n := rng.Intn(maxKeys + 1)
	for i := 0; i < n; i++ {
		key := stringFromInt(rng.Intn(8))
		vc[key] = int64(rng.Intn(100))
	}
	return vc
}

func stringFromInt(i int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz"
	if i < len(letters) {
		return string(letters[i])
	}
	return string(letters[i%len(letters)]) + stringFromInt(i/len(letters))
}
