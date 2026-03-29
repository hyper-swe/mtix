// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestGetAncestorChain_RootNode_ReturnsSelf verifies root node returns chain of 1.
// Per FR-12.2, the chain includes the node itself in root-first order.
func TestGetAncestorChain_RootNode_ReturnsSelf(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Root Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	chain, err := s.GetAncestorChain(ctx, "PROJ-1")
	require.NoError(t, err)

	require.Len(t, chain, 1)
	assert.Equal(t, "PROJ-1", chain[0].ID)
	assert.Equal(t, "", chain[0].ParentID)
}

// TestGetAncestorChain_ThreeLevels_ReturnsRootFirst verifies grandchild returns [root, parent, child]
// in root-first order per FR-12.2 (chain ordered from root to target node inclusive).
func TestGetAncestorChain_ThreeLevels_ReturnsRootFirst(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create root.
	root := makeRootNode("PROJ-1", "PROJ", "Root", now)
	require.NoError(t, s.CreateNode(ctx, root))

	// Create parent (child of root).
	parent := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Parent", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create child (child of parent).
	child := makeChildNode("PROJ-1.1.1", "PROJ-1.1", "PROJ", "Child", 2, 1, now)
	require.NoError(t, s.CreateNode(ctx, child))

	// Get ancestor chain for the grandchild.
	chain, err := s.GetAncestorChain(ctx, "PROJ-1.1.1")
	require.NoError(t, err)

	// Should return [root, parent, child] in that order.
	require.Len(t, chain, 3)
	assert.Equal(t, "PROJ-1", chain[0].ID, "first should be root")
	assert.Equal(t, "PROJ-1.1", chain[1].ID, "second should be parent")
	assert.Equal(t, "PROJ-1.1.1", chain[2].ID, "third should be child (target)")
}

// TestGetAncestorChain_TwoLevels_ReturnsRootAndParent verifies parent-child chain.
func TestGetAncestorChain_TwoLevels_ReturnsRootAndParent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	root := makeRootNode("PROJ-1", "PROJ", "Root", now)
	require.NoError(t, s.CreateNode(ctx, root))

	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child))

	chain, err := s.GetAncestorChain(ctx, "PROJ-1.1")
	require.NoError(t, err)

	require.Len(t, chain, 2)
	assert.Equal(t, "PROJ-1", chain[0].ID)
	assert.Equal(t, "PROJ-1.1", chain[1].ID)
}

// TestGetAncestorChain_NotFound_ReturnsError verifies nonexistent node returns error.
func TestGetAncestorChain_NotFound_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetAncestorChain(ctx, "NONEXISTENT-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestGetAncestorChain_DeepHierarchy_ReturnsComplete verifies deep hierarchies are fully walked.
// Tests a 5-level deep hierarchy to ensure the recursive parent-following logic works correctly.
func TestGetAncestorChain_DeepHierarchy_ReturnsComplete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create 5-level hierarchy: PROJ-1 -> PROJ-1.1 -> PROJ-1.1.1 -> PROJ-1.1.1.1 -> PROJ-1.1.1.1.1
	nodeIDs := []string{
		"PROJ-1",
		"PROJ-1.1",
		"PROJ-1.1.1",
		"PROJ-1.1.1.1",
		"PROJ-1.1.1.1.1",
	}
	parentIDs := []string{
		"",
		"PROJ-1",
		"PROJ-1.1",
		"PROJ-1.1.1",
		"PROJ-1.1.1.1",
	}
	depths := []int{0, 1, 2, 3, 4}

	for i, id := range nodeIDs {
		if i == 0 {
			node := makeRootNode(id, "PROJ", "Level "+fmtInt(i), now)
			require.NoError(t, s.CreateNode(ctx, node))
		} else {
			node := makeChildNode(id, parentIDs[i], "PROJ", "Level "+fmtInt(i), depths[i], 1, now)
			require.NoError(t, s.CreateNode(ctx, node))
		}
	}

	// Get chain for the deepest node.
	chain, err := s.GetAncestorChain(ctx, "PROJ-1.1.1.1.1")
	require.NoError(t, err)

	// Should return all 5 nodes in root-first order.
	require.Len(t, chain, 5)
	for i, id := range nodeIDs {
		assert.Equal(t, id, chain[i].ID, "position %d should be %s", i, id)
	}
}

// TestGetSiblings_WithSiblings_ReturnsSorted returns siblings excluding self, ordered by seq.
func TestGetSiblings_WithSiblings_ReturnsSorted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create parent.
	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create siblings with different seq values.
	for i := 1; i <= 3; i++ {
		child := makeChildNode(
			"PROJ-1."+fmtInt(i), "PROJ-1", "PROJ",
			"Child "+fmtInt(i), 1, i, now,
		)
		require.NoError(t, s.CreateNode(ctx, child))
	}

	// Get siblings of PROJ-1.2.
	siblings, err := s.GetSiblings(ctx, "PROJ-1.2")
	require.NoError(t, err)

	// Should return PROJ-1.1 and PROJ-1.3 (excluding self) ordered by seq.
	require.Len(t, siblings, 2)
	assert.Equal(t, "PROJ-1.1", siblings[0].ID)
	assert.Equal(t, 1, siblings[0].Seq)
	assert.Equal(t, "PROJ-1.3", siblings[1].ID)
	assert.Equal(t, 3, siblings[1].Seq)
}

// TestGetSiblings_RootNode_ReturnsNil verifies root node returns nil (no parent, no siblings).
func TestGetSiblings_RootNode_ReturnsNil(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Root", now)
	require.NoError(t, s.CreateNode(ctx, node))

	siblings, err := s.GetSiblings(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Nil(t, siblings)
}

// TestGetSiblings_OnlyChild_ReturnsEmpty returns empty slice when node is only child.
func TestGetSiblings_OnlyChild_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create only child.
	child := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Only Child", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child))

	siblings, err := s.GetSiblings(ctx, "PROJ-1.1")
	require.NoError(t, err)
	assert.Empty(t, siblings)
}

// TestGetSiblings_NotFound_ReturnsError verifies nonexistent node returns error.
func TestGetSiblings_NotFound_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.GetSiblings(ctx, "NONEXISTENT-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestGetSiblings_ExcludesDeleted verifies soft-deleted siblings are excluded per data integrity.
func TestGetSiblings_ExcludesDeleted(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create three siblings.
	for i := 1; i <= 3; i++ {
		child := makeChildNode(
			"PROJ-1."+fmtInt(i), "PROJ-1", "PROJ",
			"Child "+fmtInt(i), 1, i, now,
		)
		require.NoError(t, s.CreateNode(ctx, child))
	}

	// Soft-delete PROJ-1.2.
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.2", false, "admin"))

	// Get siblings of PROJ-1.1.
	siblings, err := s.GetSiblings(ctx, "PROJ-1.1")
	require.NoError(t, err)

	// Should only return PROJ-1.3 (PROJ-1.2 is soft-deleted).
	require.Len(t, siblings, 1)
	assert.Equal(t, "PROJ-1.3", siblings[0].ID)
}

// TestGetSiblings_MultipleChildrenUnordered_ReturnsSortedBySeq verifies proper seq ordering
// even if nodes were created in non-seq order.
func TestGetSiblings_MultipleChildrenUnordered_ReturnsSortedBySeq(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	// Create siblings out of order by seq (create seq 3, then 1, then 2).
	seqOrder := []int{3, 1, 2}
	for _, seq := range seqOrder {
		child := makeChildNode(
			"PROJ-1."+fmtInt(seq), "PROJ-1", "PROJ",
			"Child Seq "+fmtInt(seq), 1, seq, now,
		)
		require.NoError(t, s.CreateNode(ctx, child))
	}

	// Get siblings of PROJ-1.1.
	siblings, err := s.GetSiblings(ctx, "PROJ-1.1")
	require.NoError(t, err)

	// Should return in seq order: 2, 3 (excluding the queried node with seq 1).
	require.Len(t, siblings, 2)
	assert.Equal(t, 2, siblings[0].Seq)
	assert.Equal(t, 3, siblings[1].Seq)
}

// TestSetAnnotations_AddsAnnotations verifies annotations are set and readable on re-read.
func TestSetAnnotations_AddsAnnotations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Node for Annotations", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Set annotations.
	annotations := []model.Annotation{
		{
			ID:        "ann-1",
			Author:    "user@example.com",
			Text:      "First annotation",
			CreatedAt: now,
			Resolved:  false,
		},
		{
			ID:        "ann-2",
			Author:    "agent@example.com",
			Text:      "Second annotation",
			CreatedAt: now.Add(time.Second),
			Resolved:  true,
		},
	}

	err := s.SetAnnotations(ctx, "PROJ-1", annotations)
	require.NoError(t, err)

	// Verify by re-reading the node.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	require.NotNil(t, got.Annotations)
	require.Len(t, got.Annotations, 2)
	assert.Equal(t, "ann-1", got.Annotations[0].ID)
	assert.Equal(t, "user@example.com", got.Annotations[0].Author)
	assert.Equal(t, "First annotation", got.Annotations[0].Text)
	assert.False(t, got.Annotations[0].Resolved)

	assert.Equal(t, "ann-2", got.Annotations[1].ID)
	assert.Equal(t, "agent@example.com", got.Annotations[1].Author)
	assert.Equal(t, "Second annotation", got.Annotations[1].Text)
	assert.True(t, got.Annotations[1].Resolved)
}

// TestSetAnnotations_EmptySlice_ClearsAnnotations verifies empty slice clears existing annotations.
func TestSetAnnotations_EmptySlice_ClearsAnnotations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Node for Clear", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Set initial annotations.
	initialAnnotations := []model.Annotation{
		{
			ID:        "ann-1",
			Author:    "user@example.com",
			Text:      "To be cleared",
			CreatedAt: now,
			Resolved:  false,
		},
	}
	require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", initialAnnotations))

	// Verify annotations were set.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Len(t, got.Annotations, 1)

	// Clear with empty slice.
	err = s.SetAnnotations(ctx, "PROJ-1", []model.Annotation{})
	require.NoError(t, err)

	// Verify annotations are cleared.
	got, err = s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Nil(t, got.Annotations)
}

// TestSetAnnotations_UpdatesExisting verifies replacing annotations with new set.
func TestSetAnnotations_UpdatesExisting(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Node for Update", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Set initial annotations.
	initialAnnotations := []model.Annotation{
		{
			ID:        "ann-1",
			Author:    "user@example.com",
			Text:      "Original",
			CreatedAt: now,
			Resolved:  false,
		},
	}
	require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", initialAnnotations))

	// Replace with new annotations.
	newAnnotations := []model.Annotation{
		{
			ID:        "ann-2",
			Author:    "agent@example.com",
			Text:      "New annotation 1",
			CreatedAt: now.Add(2 * time.Second),
			Resolved:  true,
		},
		{
			ID:        "ann-3",
			Author:    "user2@example.com",
			Text:      "New annotation 2",
			CreatedAt: now.Add(3 * time.Second),
			Resolved:  false,
		},
	}
	require.NoError(t, s.SetAnnotations(ctx, "PROJ-1", newAnnotations))

	// Verify only new annotations exist.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	require.NotNil(t, got.Annotations)
	require.Len(t, got.Annotations, 2)
	assert.Equal(t, "ann-2", got.Annotations[0].ID)
	assert.Equal(t, "ann-3", got.Annotations[1].ID)
	// Original ann-1 should be gone.
	for _, ann := range got.Annotations {
		assert.NotEqual(t, "ann-1", ann.ID)
	}
}

// TestSetAnnotations_NonexistentNode_NoError verifies no error for nonexistent node.
// (UPDATE with no matching rows succeeds with 0 rows affected per SQLite semantics.)
func TestSetAnnotations_NonexistentNode_NoError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	annotations := []model.Annotation{
		{
			ID:        "ann-1",
			Author:    "user@example.com",
			Text:      "Orphan annotation",
			CreatedAt: now,
			Resolved:  false,
		},
	}

	// No error should occur for nonexistent node.
	err := s.SetAnnotations(ctx, "NONEXISTENT-1", annotations)
	assert.NoError(t, err)
}

// TestSetAnnotations_SoftDeletedNode_NoUpdate verifies annotations not set on soft-deleted nodes.
// Per data integrity, we filter out deleted_at IS NULL in the UPDATE, so deleted nodes are unaffected.
func TestSetAnnotations_SoftDeletedNode_NoUpdate(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Node to Delete", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Soft-delete the node.
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	// Attempt to set annotations on the deleted node.
	annotations := []model.Annotation{
		{
			ID:        "ann-1",
			Author:    "user@example.com",
			Text:      "Should not persist",
			CreatedAt: now,
			Resolved:  false,
		},
	}

	// No error (UPDATE with 0 affected rows is silent).
	err := s.SetAnnotations(ctx, "PROJ-1", annotations)
	assert.NoError(t, err)

	// Verify the node is still deleted (GetNode should fail with ErrNotFound).
	_, err = s.GetNode(ctx, "PROJ-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestSetAnnotations_LargeAnnotationSet verifies handling of multiple annotations.
func TestSetAnnotations_LargeAnnotationSet(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Node for Many Annotations", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Create 10 annotations.
	var annotations []model.Annotation
	for i := 1; i <= 10; i++ {
		annotations = append(annotations, model.Annotation{
			ID:        "ann-" + fmtInt(i),
			Author:    "user" + fmtInt(i) + "@example.com",
			Text:      "Annotation " + fmtInt(i),
			CreatedAt: now.Add(time.Duration(i) * time.Second),
			Resolved:  i%2 == 0, // Alternate resolved/unresolved.
		})
	}

	err := s.SetAnnotations(ctx, "PROJ-1", annotations)
	require.NoError(t, err)

	// Verify all 10 were stored.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)

	require.NotNil(t, got.Annotations)
	require.Len(t, got.Annotations, 10)

	// Spot-check a few.
	assert.Equal(t, "ann-1", got.Annotations[0].ID)
	assert.False(t, got.Annotations[0].Resolved)

	assert.Equal(t, "ann-10", got.Annotations[9].ID)
	assert.True(t, got.Annotations[9].Resolved)
}

// fmtInt converts int to string for test node IDs and titles.
func fmtInt(i int) string {
	return strconv.Itoa(i)
}
