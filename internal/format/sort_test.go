// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestNaturalNodeIDLess verifies the natural dot-notation comparator
// per FR-17.6. Each case is a pair (a, b) where a < b must be true.
func TestNaturalNodeIDLess(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		{"same id", "PROJ-1", "PROJ-1", false},
		{"single digit ascending", "PROJ-1", "PROJ-2", true},
		{"single digit descending", "PROJ-2", "PROJ-1", false},
		{"multi-digit: 2 before 10", "PROJ-2", "PROJ-10", true},
		{"multi-digit: 10 after 2", "PROJ-10", "PROJ-2", false},
		{"multi-digit: 9 before 10", "PROJ-9", "PROJ-10", true},
		{"multi-digit: 11 after 10", "PROJ-10", "PROJ-11", true},
		{"child dot: 1.2 before 1.10", "PROJ-1.2", "PROJ-1.10", true},
		{"child dot: 1.10 after 1.2", "PROJ-1.10", "PROJ-1.2", false},
		{"child dot: 1.1 before 1.2", "PROJ-1.1", "PROJ-1.2", true},
		{"parent before child: 1 < 1.1", "PROJ-1", "PROJ-1.1", true},
		{"child after parent: 1.1 > 1", "PROJ-1.1", "PROJ-1", false},
		{"deep: 1.1.1 before 1.1.2", "PROJ-1.1.1", "PROJ-1.1.2", true},
		{"deep: 1.1.10 after 1.1.2", "PROJ-1.1.2", "PROJ-1.1.10", true},
		{"deep: 1.2.1 after 1.1.999", "PROJ-1.1.999", "PROJ-1.2.1", true},
		{"different prefix: AAA < BBB", "AAA-1", "BBB-1", true},
		{"different prefix: BBB > AAA", "BBB-1", "AAA-1", false},
		{"same prefix different root", "PROJ-1", "PROJ-2", true},
		{"three digit segments", "PROJ-100", "PROJ-200", true},
		{"zero padded treated as numeric", "PROJ-01", "PROJ-2", true},
		{"mixed: PROJ-1.1.1.1 < PROJ-1.1.1.2", "PROJ-1.1.1.1", "PROJ-1.1.1.2", true},
		{"sibling subtrees ordered", "PROJ-2.1", "PROJ-3.1", true},
		{"empty vs non-empty", "", "PROJ-1", true},
		{"non-empty vs empty", "PROJ-1", "", false},
		{"both empty", "", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NaturalNodeIDLess(tt.a, tt.b)
			assert.Equal(t, tt.want, got, "NaturalNodeIDLess(%q, %q)", tt.a, tt.b)
		})
	}
}

// TestSortNodeIDs verifies that sorting a slice of IDs with
// NaturalNodeIDLess produces the expected natural order.
func TestSortNodeIDs(t *testing.T) {
	ids := []string{
		"PROJ-11",
		"PROJ-2",
		"PROJ-1.10",
		"PROJ-1.2",
		"PROJ-10",
		"PROJ-1",
		"PROJ-1.1",
		"PROJ-2.100",
		"PROJ-2.3",
		"PROJ-1.11",
		"PROJ-2.21",
	}

	sort.Slice(ids, func(i, j int) bool {
		return NaturalNodeIDLess(ids[i], ids[j])
	})

	expected := []string{
		"PROJ-1",
		"PROJ-1.1",
		"PROJ-1.2",
		"PROJ-1.10",
		"PROJ-1.11",
		"PROJ-2",
		"PROJ-2.3",
		"PROJ-2.21",
		"PROJ-2.100",
		"PROJ-10",
		"PROJ-11",
	}

	assert.Equal(t, expected, ids, "natural dot-notation sort order")
}

// TestSortNodeIDs_MixedPrefixes verifies sort across different project
// prefixes. Prefix is sorted lexicographically, then root+children
// sorted numerically.
func TestSortNodeIDs_MixedPrefixes(t *testing.T) {
	ids := []string{
		"BBB-1",
		"AAA-2",
		"AAA-10",
		"AAA-1",
		"BBB-2",
	}

	sort.Slice(ids, func(i, j int) bool {
		return NaturalNodeIDLess(ids[i], ids[j])
	})

	expected := []string{
		"AAA-1",
		"AAA-2",
		"AAA-10",
		"BBB-1",
		"BBB-2",
	}

	assert.Equal(t, expected, ids)
}

// TestSortNodeIDs_DeepHierarchy verifies 5-level deep sorting.
func TestSortNodeIDs_DeepHierarchy(t *testing.T) {
	ids := []string{
		"P-1.2.1.1.3",
		"P-1.2.1.1.1",
		"P-1.2.1.1.10",
		"P-1.2.1.1.2",
	}

	sort.Slice(ids, func(i, j int) bool {
		return NaturalNodeIDLess(ids[i], ids[j])
	})

	expected := []string{
		"P-1.2.1.1.1",
		"P-1.2.1.1.2",
		"P-1.2.1.1.3",
		"P-1.2.1.1.10",
	}

	assert.Equal(t, expected, ids)
}

// TestSortNodes verifies that SortNodes sorts a slice of model.Node by
// natural dot-notation ID order per FR-17.6.
func TestSortNodes(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	makeNode := func(id string) *model.Node {
		return &model.Node{
			ID:          id,
			Project:     "PROJ",
			Title:       "node " + id,
			Status:      model.StatusOpen,
			Priority:    model.PriorityMedium,
			NodeType:    model.NodeTypeEpic,
			Weight:      1.0,
			ContentHash: "h",
			CreatedAt:   now,
			UpdatedAt:   now,
		}
	}

	nodes := []*model.Node{
		makeNode("PROJ-10"),
		makeNode("PROJ-2"),
		makeNode("PROJ-1.10"),
		makeNode("PROJ-1.2"),
		makeNode("PROJ-1"),
		makeNode("PROJ-1.1"),
	}

	SortNodes(nodes)

	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	assert.Equal(t, []string{
		"PROJ-1",
		"PROJ-1.1",
		"PROJ-1.2",
		"PROJ-1.10",
		"PROJ-2",
		"PROJ-10",
	}, ids)
}

// TestSortNodes_Empty verifies SortNodes handles nil and empty slices.
func TestSortNodes_Empty(t *testing.T) {
	SortNodes(nil)
	SortNodes([]*model.Node{})
}

// TestNaturalNodeIDLess_NoHyphen verifies IDs without a hyphen (edge case).
func TestNaturalNodeIDLess_NoHyphen(t *testing.T) {
	// Without a hyphen, the entire ID is treated as path segments.
	assert.True(t, NaturalNodeIDLess("1.2", "1.10"))
	assert.True(t, NaturalNodeIDLess("1", "2"))
	assert.False(t, NaturalNodeIDLess("10", "2"))
}

// TestNaturalNodeIDLess_NonNumericSegments verifies the lexicographic
// fallback when a segment is not a pure integer.
func TestNaturalNodeIDLess_NonNumericSegments(t *testing.T) {
	// Non-numeric segment falls back to string comparison.
	assert.True(t, NaturalNodeIDLess("PROJ-abc", "PROJ-def"))
	assert.False(t, NaturalNodeIDLess("PROJ-def", "PROJ-abc"))
	assert.True(t, NaturalNodeIDLess("PROJ-1.abc", "PROJ-1.def"))
}
