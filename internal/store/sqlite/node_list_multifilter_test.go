// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// TestListNodes_MultipleUnderFilter_ReturnsUnion verifies that --under
// accepts multiple subtree roots and returns the union per FR-17.1.
// Agents commonly want to scope queries to several specific epics.
func TestListNodes_MultipleUnderFilter_ReturnsUnion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Three roots, each with one child.
	require.NoError(t, s.CreateNode(ctx, makeRootNode("MUF-1", "MUF", "Root 1", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("MUF-1.1", "MUF-1", "MUF", "Child 1.1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("MUF-2", "MUF", "Root 2", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("MUF-2.1", "MUF-2", "MUF", "Child 2.1", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("MUF-3", "MUF", "Root 3", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("MUF-3.1", "MUF-3", "MUF", "Child 3.1", 1, 1, now)))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Under: []string{"MUF-1", "MUF-3"},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 4, total, "should return MUF-1, MUF-1.1, MUF-3, MUF-3.1")
	require.Len(t, nodes, 4)

	// Verify only the requested subtrees are present.
	ids := make(map[string]bool, 4)
	for _, n := range nodes {
		ids[n.ID] = true
	}
	assert.True(t, ids["MUF-1"], "MUF-1 should be present")
	assert.True(t, ids["MUF-1.1"], "MUF-1.1 should be present")
	assert.True(t, ids["MUF-3"], "MUF-3 should be present")
	assert.True(t, ids["MUF-3.1"], "MUF-3.1 should be present")
	assert.False(t, ids["MUF-2"], "MUF-2 should NOT be present")
	assert.False(t, ids["MUF-2.1"], "MUF-2.1 should NOT be present")
}

// TestListNodes_MultipleNodeTypeFilter_ReturnsUnion verifies that --type
// accepts multiple node types and returns the union per FR-17.1.
func TestListNodes_MultipleNodeTypeFilter_ReturnsUnion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create three different types.
	n1 := makeRootNode("MNT-1", "MNT", "Epic", now)
	n1.NodeType = model.NodeTypeEpic
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("MNT-2", "MNT", "Story", now)
	n2.NodeType = model.NodeTypeStory
	require.NoError(t, s.CreateNode(ctx, n2))

	n3 := makeRootNode("MNT-3", "MNT", "Issue", now)
	n3.NodeType = model.NodeTypeIssue
	require.NoError(t, s.CreateNode(ctx, n3))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		NodeType: []string{string(model.NodeTypeEpic), string(model.NodeTypeIssue)},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, nodes, 2)

	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.ID] = true
	}
	assert.True(t, ids["MNT-1"])
	assert.True(t, ids["MNT-3"])
	assert.False(t, ids["MNT-2"], "story should NOT be present")
}

// TestListNodes_MultiplePriorityFilter_ReturnsUnion verifies that --priority
// accepts multiple values and returns the union per FR-17.1.
func TestListNodes_MultiplePriorityFilter_ReturnsUnion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	priorities := []model.Priority{
		model.PriorityCritical, // 1
		model.PriorityHigh,     // 2
		model.PriorityMedium,   // 3
		model.PriorityLow,      // 4
	}
	for i, p := range priorities {
		n := makeRootNode("MPF-"+string(rune('0'+i+1)), "MPF", "Node", now)
		n.Priority = p
		require.NoError(t, s.CreateNode(ctx, n))
	}

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Priority: []int{int(model.PriorityCritical), int(model.PriorityHigh)},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total, "should return only priority 1 and 2 nodes")
	require.Len(t, nodes, 2)
}

// TestListNodes_MultipleAssigneeFilter_ReturnsUnion verifies that --assignee
// accepts multiple values and returns the union per FR-17.1.
func TestListNodes_MultipleAssigneeFilter_ReturnsUnion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("MAF-1", "MAF", "A", now)))
	require.NoError(t, s.ClaimNode(ctx, "MAF-1", "agent-a"))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("MAF-2", "MAF", "B", now)))
	require.NoError(t, s.ClaimNode(ctx, "MAF-2", "agent-b"))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("MAF-3", "MAF", "C", now)))
	require.NoError(t, s.ClaimNode(ctx, "MAF-3", "agent-c"))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Assignee: []string{"agent-a", "agent-c"},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, nodes, 2)

	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.ID] = true
	}
	assert.True(t, ids["MAF-1"])
	assert.True(t, ids["MAF-3"])
	assert.False(t, ids["MAF-2"])
}

// TestListNodes_CombinedMultiFilters_ANDedTogether verifies that multiple
// flags AND combine while values within one flag OR combine per FR-17.1.
func TestListNodes_CombinedMultiFilters_ANDedTogether(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Three nodes — only one matches BOTH filters.
	n1 := makeRootNode("CMF-1", "CMF", "Match", now)
	n1.NodeType = model.NodeTypeEpic
	n1.Priority = model.PriorityCritical
	require.NoError(t, s.CreateNode(ctx, n1))

	n2 := makeRootNode("CMF-2", "CMF", "Wrong type", now)
	n2.NodeType = model.NodeTypeStory
	n2.Priority = model.PriorityCritical
	require.NoError(t, s.CreateNode(ctx, n2))

	n3 := makeRootNode("CMF-3", "CMF", "Wrong priority", now)
	n3.NodeType = model.NodeTypeEpic
	n3.Priority = model.PriorityLow
	require.NoError(t, s.CreateNode(ctx, n3))

	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{
		NodeType: []string{string(model.NodeTypeEpic), string(model.NodeTypeIssue)},
		Priority: []int{int(model.PriorityCritical), int(model.PriorityHigh)},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "CMF-1", nodes[0].ID)
}

// TestListNodes_SingleValueSliceFilters_BackwardCompat verifies that
// single-value slices behave identically to old single-value strings.
// This guards against regressions when callers wrap one value as a slice.
func TestListNodes_SingleValueSliceFilters_BackwardCompat(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("SVF-1", "SVF", "Parent", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("SVF-1.1", "SVF-1", "SVF", "Child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("SVF-2", "SVF", "Other", now)))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Under: []string{"SVF-1"},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total, "should match SVF-1 and SVF-1.1")
	assert.Len(t, nodes, 2)
}

// TestListNodes_EmptySliceFilters_NoFilterApplied verifies that empty
// slices are equivalent to no filter (don't generate WHERE clauses).
func TestListNodes_EmptySliceFilters_NoFilterApplied(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("ESF-1", "ESF", "First", now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("ESF-2", "ESF", "Second", now)))

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{
		Under:    []string{},
		NodeType: []string{},
		Assignee: []string{},
		Priority: []int{},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, nodes, 2)
}

// TestBuildFilterClauses_MultiUnder_AllParameterized is the SQL injection
// regression test per FR-17.1 security audit T1. Verifies that for every
// filter type, values are passed as bound parameters and never appear in
// the clause string. This protects against future refactors accidentally
// reintroducing string concatenation.
//
// The test calls ListNodes with a malicious value containing SQL
// metacharacters. If concatenation were used, this would inject SQL.
// Since we use bound parameters, the value is treated as a literal and
// matches no rows.
func TestListNodes_MultiUnder_SQLInjectionResistance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("INJ-1", "INJ", "Real", now)))

	// Classic injection payload.
	maliciousUnder := []string{"INJ-1'; DROP TABLE nodes; --", "INJ-1"}
	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{
		Under: maliciousUnder,
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err, "must not error — bound parameter treats string as literal")
	require.Len(t, nodes, 1, "should match only the legitimate INJ-1")
	assert.Equal(t, "INJ-1", nodes[0].ID)

	// Verify the table still exists by listing again.
	_, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 50})
	require.NoError(t, err, "table should still exist after attempted injection")
	assert.Equal(t, 1, total)
}

// TestListNodes_MultiNodeType_SQLInjectionResistance protects --type from
// the same injection class.
func TestListNodes_MultiNodeType_SQLInjectionResistance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	n := makeRootNode("INJ-1", "INJ", "Real", now)
	n.NodeType = model.NodeTypeEpic
	require.NoError(t, s.CreateNode(ctx, n))

	maliciousType := []string{"epic' OR 1=1 --", "epic"}
	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{
		NodeType: maliciousType,
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	require.Len(t, nodes, 1, "literal match should find one node")
	assert.Equal(t, "INJ-1", nodes[0].ID)
}

// TestListNodes_MultiAssignee_SQLInjectionResistance protects --assignee
// from injection.
func TestListNodes_MultiAssignee_SQLInjectionResistance(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("INJ-1", "INJ", "Real", now)))
	require.NoError(t, s.ClaimNode(ctx, "INJ-1", "agent-a"))

	maliciousAssignee := []string{"agent-a' OR 1=1 --", "agent-a"}
	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{
		Assignee: maliciousAssignee,
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "INJ-1", nodes[0].ID)
}
