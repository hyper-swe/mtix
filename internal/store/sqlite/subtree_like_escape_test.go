// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-95.17 regressions (review F-60): every subtree query binds a node id
// into a LIKE pattern (id + ".%"). The sync project_prefix grammar admits
// '_', which LIKE reads as a single-character wildcard, so an unescaped
// "A_B-1.%" also matched "AXB-1.1", a node of another project. Each test
// seeds two projects whose prefixes differ only at the wildcard position and
// asserts that an operation on "A_B-1" never reaches "AXB-1.*".
package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Fixture ids. likeRoot's subtree is the target of every operation.
// likeSibling shares the text "A_B-1" but is not a descendant (it guards the
// literal '.' separator). likeLookalike* belong to project AXB, which an
// unescaped "A_B-1.%" pattern matches through the '_' wildcard.
const (
	likeRoot             = "A_B-1"
	likeChild            = "A_B-1.1"
	likeGrandchild       = "A_B-1.1.1"
	likeSibling          = "A_B-10"
	likeLookalikeRoot    = "AXB-1"
	likeLookalikeChild   = "AXB-1.1"
	likeSearchNeedle     = "wildcardneedle"
	likeTestActor        = "like-tester"
	likeLookalikeDeleter = "lookalike-owner"
)

// likeOwnSubtree is the exact subtree of likeRoot, in GetTree order.
var likeOwnSubtree = []string{likeRoot, likeChild, likeGrandchild}

// seedWildcardLookalikes creates the A_B / AXB fixture in a fresh store.
// Every title contains likeSearchNeedle so full-text search matches them all.
func seedWildcardLookalikes(t *testing.T) *sqlite.Store {
	t.Helper()
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)

	sibling := makeRootNode(likeSibling, "A_B", likeSearchNeedle+" sibling", now)
	sibling.Seq = 10
	nodes := []*model.Node{
		makeRootNode(likeRoot, "A_B", likeSearchNeedle+" root", now),
		makeChildNode(likeChild, likeRoot, "A_B", likeSearchNeedle+" child", 1, 1, now),
		makeChildNode(likeGrandchild, likeChild, "A_B", likeSearchNeedle+" grandchild", 2, 1, now),
		sibling,
		makeRootNode(likeLookalikeRoot, "AXB", likeSearchNeedle+" lookalike root", now),
		makeChildNode(likeLookalikeChild, likeLookalikeRoot, "AXB", likeSearchNeedle+" lookalike child", 1, 1, now),
	}
	for _, n := range nodes {
		require.NoError(t, s.CreateNode(ctx, n), "seed %s", n.ID)
	}
	return s
}

// likeIsDeleted reports whether id is soft-deleted, reading the raw row so
// deleted nodes are visible.
func likeIsDeleted(t *testing.T, s *sqlite.Store, id string) bool {
	t.Helper()
	var deletedAt sql.NullString
	err := s.QueryRow(context.Background(),
		"SELECT deleted_at FROM nodes WHERE id = ?", id).Scan(&deletedAt)
	require.NoError(t, err, "read deleted_at of %s", id)
	return deletedAt.Valid
}

// likeStatusOf returns the raw status column of id.
func likeStatusOf(t *testing.T, s *sqlite.Store, id string) model.Status {
	t.Helper()
	var status string
	err := s.QueryRow(context.Background(),
		"SELECT status FROM nodes WHERE id = ?", id).Scan(&status)
	require.NoError(t, err, "read status of %s", id)
	return model.Status(status)
}

// likeNodeIDs projects a node slice onto its ids.
func likeNodeIDs(nodes []*model.Node) []string {
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	return ids
}

// likeSum totals the counts of a grouped stats map.
func likeSum(counts map[string]int) int {
	total := 0
	for _, c := range counts {
		total += c
	}
	return total
}

// TestDeleteNode_CascadeUnderscorePrefix_SparesLookalikeProject verifies that
// a cascade delete of "A_B-1" soft-deletes its own descendants and leaves
// "AXB-1.1" (and the sibling "A_B-10") live (MTIX-95.17, story 1).
func TestDeleteNode_CascadeUnderscorePrefix_SparesLookalikeProject(t *testing.T) {
	s := seedWildcardLookalikes(t)
	require.NoError(t, s.DeleteNode(context.Background(), likeRoot, true, likeTestActor))

	tests := []struct {
		id          string
		wantDeleted bool
	}{
		{likeRoot, true},
		{likeChild, true},
		{likeGrandchild, true},
		{likeSibling, false},
		{likeLookalikeRoot, false},
		{likeLookalikeChild, false},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			assert.Equal(t, tt.wantDeleted, likeIsDeleted(t, s, tt.id))
		})
	}
}

// TestUndeleteNode_UnderscorePrefix_LeavesLookalikeDeleted verifies that
// undeleting "A_B-1" restores its own descendants only. "AXB-1.1" and the
// sibling "A_B-10" were deleted independently beforehand and must stay
// deleted (MTIX-95.17, story 2). The look-alike is deleted BEFORE A_B-1 so
// this test does not depend on the cascade-delete fix.
func TestUndeleteNode_UnderscorePrefix_LeavesLookalikeDeleted(t *testing.T) {
	s := seedWildcardLookalikes(t)
	ctx := context.Background()
	require.NoError(t, s.DeleteNode(ctx, likeLookalikeChild, false, likeLookalikeDeleter))
	require.NoError(t, s.DeleteNode(ctx, likeSibling, false, likeLookalikeDeleter))
	require.NoError(t, s.DeleteNode(ctx, likeRoot, true, likeTestActor))

	require.NoError(t, s.UndeleteNode(ctx, likeRoot))

	tests := []struct {
		id          string
		wantDeleted bool
	}{
		{likeRoot, false},
		{likeChild, false},
		{likeGrandchild, false},
		{likeSibling, true},
		{likeLookalikeRoot, false},
		{likeLookalikeChild, true},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			assert.Equal(t, tt.wantDeleted, likeIsDeleted(t, s, tt.id))
		})
	}
}

// TestCancelNode_CascadeUnderscorePrefix_SparesLookalikeProject verifies that
// a cascade cancel of "A_B-1" cancels its own descendants and leaves
// "AXB-1.1" (and the sibling "A_B-10") open (MTIX-95.17, story 3).
func TestCancelNode_CascadeUnderscorePrefix_SparesLookalikeProject(t *testing.T) {
	s := seedWildcardLookalikes(t)
	require.NoError(t, s.CancelNode(context.Background(), likeRoot, "scope dropped", likeTestActor, true))

	tests := []struct {
		id         string
		wantStatus model.Status
	}{
		{likeRoot, model.StatusCancelled},
		{likeChild, model.StatusCancelled},
		{likeGrandchild, model.StatusCancelled},
		{likeSibling, model.StatusOpen},
		{likeLookalikeRoot, model.StatusOpen},
		{likeLookalikeChild, model.StatusOpen},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			assert.Equal(t, tt.wantStatus, likeStatusOf(t, s, tt.id))
		})
	}
}

// TestGetTree_UnderscorePrefix_ReturnsOwnSubtreeOnly verifies that GetTree
// for "A_B-1" returns exactly its own subtree (MTIX-95.17, story 4).
func TestGetTree_UnderscorePrefix_ReturnsOwnSubtreeOnly(t *testing.T) {
	s := seedWildcardLookalikes(t)

	nodes, err := s.GetTree(context.Background(), likeRoot, 10)
	require.NoError(t, err)
	assert.Equal(t, likeOwnSubtree, likeNodeIDs(nodes))
}

// TestGetStats_UnderscoreScope_CountsOwnSubtreeOnly verifies that stats
// scoped to "A_B-1" count only its own three nodes, in the total and in the
// grouped counts (MTIX-95.17, story 5).
func TestGetStats_UnderscoreScope_CountsOwnSubtreeOnly(t *testing.T) {
	s := seedWildcardLookalikes(t)

	stats, err := s.GetStats(context.Background(), likeRoot)
	require.NoError(t, err)

	tests := []struct {
		name string
		got  int
	}{
		{"total", stats.TotalNodes},
		{"by_status_open", stats.ByStatus[string(model.StatusOpen)]},
		{"by_priority_3_medium", stats.ByPriority["3"]},
		{"by_type_sum", likeSum(stats.ByType)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, len(likeOwnSubtree), tt.got)
		})
	}
}

// TestBuildFilterClauses_UnderUnderscorePrefix_MatchesOwnSubtreeOnly verifies
// that the Under filter for "A_B-1" matches only its own subtree, through
// both callers of buildFilterClausesWithPrefix: ListNodes (unaliased) and
// SearchNodes (aliased "n.") (MTIX-95.17, story 6).
func TestBuildFilterClauses_UnderUnderscorePrefix_MatchesOwnSubtreeOnly(t *testing.T) {
	s := seedWildcardLookalikes(t)
	ctx := context.Background()
	filter := store.NodeFilter{Under: []string{likeRoot}}
	opts := store.ListOptions{Limit: 100}

	tests := []struct {
		name string
		run  func() ([]*model.Node, int, error)
	}{
		{"list", func() ([]*model.Node, int, error) { return s.ListNodes(ctx, filter, opts) }},
		{"search", func() ([]*model.Node, int, error) {
			return s.SearchNodes(ctx, likeSearchNeedle, filter, opts)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes, total, err := tt.run()
			require.NoError(t, err)
			assert.ElementsMatch(t, likeOwnSubtree, likeNodeIDs(nodes))
			assert.Equal(t, len(likeOwnSubtree), total)
		})
	}
}
