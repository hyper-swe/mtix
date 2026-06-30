// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedTwoProjects seeds a fresh store with two projects: AAA (AAA-1, AAA-1.1)
// and BBB (BBB-1). Returns the store and a context for further assertions.
func seedTwoProjects(t *testing.T) (*sqlite.Store, context.Context) {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, makeRootNode("AAA-1", "AAA", "Alpha root", now)))
	require.NoError(t, s.CreateNode(ctx, makeChildNode("AAA-1.1", "AAA-1", "AAA", "Alpha child", 1, 1, now)))
	require.NoError(t, s.CreateNode(ctx, makeRootNode("BBB-1", "BBB", "Bravo root", now)))

	return s, ctx
}

// TestListNodes_ProjectFilter_RestrictsToProject verifies MP-3: a non-empty
// NodeFilter.Project restricts results to that exact project prefix.
func TestListNodes_ProjectFilter_RestrictsToProject(t *testing.T) {
	s, ctx := seedTwoProjects(t)

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{Project: "AAA"}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 2, total)
	require.Len(t, nodes, 2)
	for _, n := range nodes {
		assert.Equal(t, "AAA", n.Project)
	}
}

// TestListNodes_EmptyProject_ReturnsAllProjects verifies MP-3: an empty
// NodeFilter.Project applies no project filter (all projects returned).
func TestListNodes_EmptyProject_ReturnsAllProjects(t *testing.T) {
	s, ctx := seedTwoProjects(t)

	nodes, total, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Equal(t, 3, total)
	assert.Len(t, nodes, 3)
}

// TestDistinctProjects_ReturnsProjectsWithCounts verifies MP-4: DistinctProjects
// returns the distinct live projects ordered by prefix with per-project counts.
func TestDistinctProjects_ReturnsProjectsWithCounts(t *testing.T) {
	s, ctx := seedTwoProjects(t)

	projects, err := s.DistinctProjects(ctx)
	require.NoError(t, err)
	require.Len(t, projects, 2)

	assert.Equal(t, store.ProjectInfo{Prefix: "AAA", Count: 2}, projects[0])
	assert.Equal(t, store.ProjectInfo{Prefix: "BBB", Count: 1}, projects[1])
}

// TestDistinctProjects_ExcludesSoftDeleted verifies MP-4 counts are consistent
// with ListNodes: soft-deleted nodes are not counted, and a project that
// becomes empty drops out of the result.
func TestDistinctProjects_ExcludesSoftDeleted(t *testing.T) {
	s, ctx := seedTwoProjects(t)

	require.NoError(t, s.DeleteNode(ctx, "BBB-1", false, "test"))

	projects, err := s.DistinctProjects(ctx)
	require.NoError(t, err)
	require.Len(t, projects, 1)
	assert.Equal(t, store.ProjectInfo{Prefix: "AAA", Count: 2}, projects[0])
}

// TestDistinctProjects_Empty verifies an empty store yields no projects.
func TestDistinctProjects_Empty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	projects, err := s.DistinctProjects(ctx)
	require.NoError(t, err)
	assert.Empty(t, projects)
}
