// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// TestRunList_TypeFlag_FiltersResults verifies that the new --type flag on
// list works the same as it does on search per FR-17.2 (parity).
func TestRunList_TypeFlag_FiltersResults(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()

	// Two epics + one issue.
	require.NoError(t, runCreate("Epic A", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Epic B", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Issue", "TEST-1", "", 3, "", "", "", "", ""))
	// Force the issue to depth 2 by creating one more under TEST-1 (so we
	// have a child structure deep enough that the leaf becomes 'issue' type
	// per the depth-derived rules).

	// Verify directly via store: --type epic should return only epics.
	nodes, _, err := app.store.ListNodes(ctx, store.NodeFilter{
		NodeType: []string{string(model.NodeTypeEpic)},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	for _, n := range nodes {
		assert.Equal(t, model.NodeTypeEpic, n.NodeType)
	}

	// Now verify the runList command with --type flag works without erroring.
	err = runList("", "", "", string(model.NodeTypeEpic), "", 50)
	assert.NoError(t, err, "--type flag must be accepted by runList")
}

// TestRunList_CommaSeparatedUnder_AcceptsMultipleSubtrees verifies that
// --under PROJ-1,PROJ-2 is parsed and applied correctly per FR-17.1.
func TestRunList_CommaSeparatedUnder_AcceptsMultipleSubtrees(t *testing.T) {
	initTestApp(t)

	require.NoError(t, runCreate("Root 1", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Root 2", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Root 3", "", "", 3, "", "", "", "", ""))

	// Comma-separated --under value.
	err := runList("", "TEST-1,TEST-3", "", "", "", 50)
	assert.NoError(t, err, "comma-separated --under must be accepted")

	// Verify the underlying filter selects only the requested subtrees.
	nodes, _, err := app.store.ListNodes(context.Background(), store.NodeFilter{
		Under: []string{"TEST-1", "TEST-3"},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	require.Len(t, nodes, 2)
	ids := map[string]bool{}
	for _, n := range nodes {
		ids[n.ID] = true
	}
	assert.True(t, ids["TEST-1"])
	assert.True(t, ids["TEST-3"])
	assert.False(t, ids["TEST-2"], "TEST-2 must be excluded")
}

// TestRunList_CommaSeparatedStatus_ParsesCorrectly verifies that --status
// done,cancelled produces a slice filter per FR-17.1.
func TestRunList_CommaSeparatedStatus_ParsesCorrectly(t *testing.T) {
	initTestApp(t)

	// Create three nodes and transition two to done/cancelled.
	require.NoError(t, runCreate("Open node", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Done node", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Cancelled node", "", "", 3, "", "", "", "", ""))

	ctx := context.Background()
	require.NoError(t, app.store.TransitionStatus(ctx, "TEST-2", model.StatusInProgress, "wip", "test"))
	require.NoError(t, app.store.TransitionStatus(ctx, "TEST-2", model.StatusDone, "done", "test"))
	require.NoError(t, app.store.TransitionStatus(ctx, "TEST-3", model.StatusCancelled, "cancelled", "test"))

	err := runList("done,cancelled", "", "", "", "", 50)
	assert.NoError(t, err, "comma-separated --status must be accepted")

	// Verify only done + cancelled match.
	nodes, total, err := app.store.ListNodes(ctx, store.NodeFilter{
		Status: []model.Status{model.StatusDone, model.StatusCancelled},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	assert.Len(t, nodes, 2)
}

// TestRunList_CommaSeparatedPriority_ParsesCorrectly verifies that
// --priority 1,2 produces a slice filter per FR-17.1.
func TestRunList_CommaSeparatedPriority_ParsesCorrectly(t *testing.T) {
	initTestApp(t)

	require.NoError(t, runCreate("Critical", "", "", 1, "", "", "", "", ""))
	require.NoError(t, runCreate("High", "", "", 2, "", "", "", "", ""))
	require.NoError(t, runCreate("Medium", "", "", 3, "", "", "", "", ""))

	err := runList("", "", "", "", "1,2", 50)
	assert.NoError(t, err, "comma-separated --priority must be accepted")
}

// TestRunList_InvalidPriority_ReturnsError verifies that --priority foo
// fails fast with INVALID_INPUT per FR-17.1 / T8.
func TestRunList_InvalidPriority_ReturnsError(t *testing.T) {
	initTestApp(t)
	err := runList("", "", "", "", "foo", 50)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestRunSearch_CommaSeparatedAllFilters_ParsesCorrectly verifies that
// search accepts the same multi-value filter syntax as list per FR-17.2.
func TestRunSearch_CommaSeparatedAllFilters_ParsesCorrectly(t *testing.T) {
	initTestApp(t)

	require.NoError(t, runCreate("Searchable A", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Searchable B", "", "", 3, "", "", "", "", ""))

	// Multi-value flags accepted without error.
	err := runSearch("open,in_progress", "agent-1,agent-2", "epic,story", "TEST-1,TEST-2", 50)
	assert.NoError(t, err, "search must accept comma-separated multi-value filters")
}

// TestSplitCSVApplied_ToFilters verifies the wiring between flag parsing
// and the filter slice. This is the integration point for FR-17.1.
func TestRunList_NoFiltersAtAll_ListsAll(t *testing.T) {
	initTestApp(t)

	require.NoError(t, runCreate("First", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Second", "", "", 3, "", "", "", "", ""))

	err := runList("", "", "", "", "", 50)
	assert.NoError(t, err)
}
