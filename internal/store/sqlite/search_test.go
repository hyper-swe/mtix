// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// TestSearchNodes_MatchesTitle verifies FTS5 matches on title field.
func TestSearchNodes_MatchesTitle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create nodes with distinct titles.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Authentication module",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-2", Project: "S", Depth: 0, Seq: 2, Title: "Payment gateway",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	// Search for "Authentication".
	results, total, err := s.SearchNodes(ctx, "Authentication", store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, results, 1)
	assert.Equal(t, "S-1", results[0].ID)
}

// TestSearchNodes_MatchesDescription verifies FTS5 matches on description field.
func TestSearchNodes_MatchesDescription(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Task One",
		Description: "Integrate Stripe payments",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	results, total, err := s.SearchNodes(ctx, "Stripe", store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, results, 1)
	assert.Equal(t, "S-1", results[0].ID)
}

// TestSearchNodes_MatchesPrompt verifies FTS5 matches on prompt field.
func TestSearchNodes_MatchesPrompt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Task One",
		Prompt: "Implement OAuth2 authentication flows",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	results, total, err := s.SearchNodes(ctx, "OAuth2", store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, results, 1)
	assert.Equal(t, "S-1", results[0].ID)
}

// TestSearchNodes_ExcludesSoftDeleted verifies soft-deleted nodes are filtered out.
func TestSearchNodes_ExcludesSoftDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Active task findme",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-2", Project: "S", Depth: 0, Seq: 2, Title: "Deleted task findme",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	// Soft-delete S-2.
	require.NoError(t, s.DeleteNode(ctx, "S-2", false, "tester"))

	results, total, err := s.SearchNodes(ctx, "findme", store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total, "soft-deleted node should be excluded")
	require.Len(t, results, 1)
	assert.Equal(t, "S-1", results[0].ID)
}

// TestSearchNodes_EmptyQuery_ReturnsError verifies empty query is rejected.
func TestSearchNodes_EmptyQuery_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, _, err := s.SearchNodes(ctx, "", store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestSearchNodes_NoMatches_ReturnsEmpty verifies no results for unmatched query.
func TestSearchNodes_NoMatches_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Authentication module",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	results, total, err := s.SearchNodes(ctx, "zzzznonexistent", store.NodeFilter{}, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 0, total)
	assert.Nil(t, results)
}

// TestSearchNodes_WithStatusFilter verifies search respects status filter.
func TestSearchNodes_WithStatusFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Searchterm alpha",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-2", Project: "S", Depth: 0, Seq: 2, Title: "Searchterm beta",
		Status: model.StatusDone, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	// Filter for only open nodes.
	filter := store.NodeFilter{Status: []model.Status{model.StatusOpen}}
	results, total, err := s.SearchNodes(ctx, "Searchterm", filter, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, results, 1)
	assert.Equal(t, "S-1", results[0].ID)
}

// TestSearchNodes_WithAssigneeFilter verifies search respects assignee filter.
func TestSearchNodes_WithAssigneeFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Findable task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", Assignee: "agent-A",
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-2", Project: "S", Depth: 0, Seq: 2, Title: "Findable other",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2", Assignee: "agent-B",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))

	filter := store.NodeFilter{Assignee: []string{"agent-A"}}
	results, total, err := s.SearchNodes(ctx, "Findable", filter, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	require.Len(t, results, 1)
	assert.Equal(t, "S-1", results[0].ID)
}

// TestSearchNodes_Pagination verifies limit/offset work correctly.
func TestSearchNodes_Pagination(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create 5 nodes with searchable term.
	for i := 1; i <= 5; i++ {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: fmt.Sprintf("S-%d", i), Project: "S", Depth: 0, Seq: i,
			Title: fmt.Sprintf("Pageable item %d", i),
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeIssue, ContentHash: fmt.Sprintf("h%d", i),
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			UpdatedAt: now.Add(time.Duration(i) * time.Second),
		}))
	}

	// Page 1: limit 2, offset 0.
	results, total, err := s.SearchNodes(ctx, "Pageable", store.NodeFilter{}, store.ListOptions{Limit: 2, Offset: 0})
	require.NoError(t, err)
	assert.Equal(t, 5, total, "total should reflect all matches")
	assert.Len(t, results, 2, "should return only 2 results")

	// Page 3: limit 2, offset 4.
	results, total, err = s.SearchNodes(ctx, "Pageable", store.NodeFilter{}, store.ListOptions{Limit: 2, Offset: 4})
	require.NoError(t, err)
	assert.Equal(t, 5, total)
	assert.Len(t, results, 1, "last page should have 1 result")
}

// TestSearchNodes_WithUnderFilter verifies search respects subtree filter.
func TestSearchNodes_WithUnderFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Searchable root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1.1", ParentID: "S-1", Project: "S", Depth: 1, Seq: 1,
		Title: "Searchable child",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2",
		CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second),
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-2", Project: "S", Depth: 0, Seq: 2, Title: "Searchable other",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h3",
		CreatedAt: now.Add(2 * time.Second), UpdatedAt: now.Add(2 * time.Second),
	}))

	// Search within S-1 subtree only.
	filter := store.NodeFilter{Under: []string{"S-1"}}
	results, total, err := s.SearchNodes(ctx, "Searchable", filter, store.ListOptions{Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 2, total, "should find root and child in subtree")
	assert.Len(t, results, 2)
}

// TestSearchNodes_ZeroLimit_ReturnsCountOnly verifies limit=0 returns count.
func TestSearchNodes_ZeroLimit_ReturnsCountOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "S-1", Project: "S", Depth: 0, Seq: 1, Title: "Countable item",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	results, total, err := s.SearchNodes(ctx, "Countable", store.NodeFilter{}, store.ListOptions{Limit: 0})
	require.NoError(t, err)
	assert.Equal(t, 1, total)
	assert.Nil(t, results, "no rows should be returned with limit=0")
}
