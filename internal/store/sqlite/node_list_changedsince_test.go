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

// MTIX-6.1: NodeFilter.ChangedSince restricts results to nodes updated strictly
// after the given instant — the "what changed since my last check?" poll.

func csNode(id, title string, updated time.Time) *model.Node {
	return &model.Node{
		ID: id, Project: "CS", Depth: 0, Seq: int(updated.Unix() % 100000),
		Title: title, Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeIssue, ContentHash: id,
		CreatedAt: updated, UpdatedAt: updated,
	}
}

func idsOf(nodes []*model.Node) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}

func TestListNodes_ChangedSince_ReturnsOnlyNewer(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	old := time.Date(2026, 4, 6, 9, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 4, 6, 11, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, csNode("CS-1", "old", old)))
	require.NoError(t, s.CreateNode(ctx, csNode("CS-2", "recent", recent)))

	threshold := time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)
	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{ChangedSince: threshold}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"CS-2"}, idsOf(nodes),
		"only the node updated after the threshold is returned")
}

func TestListNodes_ChangedSince_ExcludesExactBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, csNode("CS-1", "at-threshold", at)))

	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{ChangedSince: at}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Empty(t, idsOf(nodes), "updated_at == threshold is excluded (strict >)")
}

func TestListNodes_ChangedSince_ZeroMeansNoFilter(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, csNode("CS-1", "a", time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))))

	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.Len(t, nodes, 1, "a zero ChangedSince applies no time filter")
}

func TestListNodes_ChangedSince_CombinesWithStatus(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	recent := time.Date(2026, 4, 6, 11, 0, 0, 0, time.UTC)

	openNode := csNode("CS-1", "recent-open", recent)
	doneNode := csNode("CS-2", "recent-done", recent)
	doneNode.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, openNode))
	require.NoError(t, s.CreateNode(ctx, doneNode))

	threshold := time.Date(2026, 4, 6, 10, 0, 0, 0, time.UTC)
	nodes, _, err := s.ListNodes(ctx, store.NodeFilter{
		ChangedSince: threshold,
		Status:       []model.Status{model.StatusOpen},
	}, store.ListOptions{Limit: 50})
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"CS-1"}, idsOf(nodes),
		"--changed-since composes with --status")
}
