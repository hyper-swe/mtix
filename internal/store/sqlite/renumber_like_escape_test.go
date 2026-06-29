// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-33 regression: RenumberSubtree's LIKE patterns must escape the id
// prefix. A project prefix may legally contain '_' (a LIKE single-char
// wildcard), so an unescaped 'DEP_ADD-1.%' would cross-match an unrelated
// same-length prefix like 'DEPXADD-1.2' and corrupt it during a renumber.
package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// likeTestChild builds a depth-1 child fixture with an explicit seq.
func likeTestChild(id, parentID, project, title string, seq int) *model.Node {
	n := mkNode(id, parentID, project, title)
	n.Depth = 1
	n.Seq = seq
	n.NodeType = model.NodeTypeForDepth(1)
	n.ContentHash = n.ComputeHash()
	return n
}

// TestRenumberSubtree_NoCrossPrefixMatch_UnderscoreWildcard renumbers a node
// in project DEP_ADD and asserts a node in the look-alike project DEPXADD
// (which the unescaped '_' wildcard would match) is left completely untouched.
func TestRenumberSubtree_NoCrossPrefixMatch_UnderscoreWildcard(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()

	// Project whose prefix contains '_'.
	require.NoError(t, s.CreateNode(ctx, mkNode("DEP_ADD-1", "", "DEP_ADD", "underscore root")))
	require.NoError(t, s.CreateNode(ctx, likeTestChild("DEP_ADD-1.1", "DEP_ADD-1", "DEP_ADD", "real child", 1)))

	// Look-alike project: 'DEPXADD-1.2' matches the LIKE pattern 'DEP_ADD-1.%'
	// when '_' is treated as a wildcard. It must NOT be touched.
	require.NoError(t, s.CreateNode(ctx, mkNode("DEPXADD-1", "", "DEPXADD", "lookalike root")))
	require.NoError(t, s.CreateNode(ctx, likeTestChild("DEPXADD-1.2", "DEPXADD-1", "DEPXADD", "DO NOT MOVE", 2)))

	// Renumber DEP_ADD-1 -> DEP_ADD-2 (root reseq). Its own subtree moves.
	require.NoError(t, s.RenumberSubtree(ctx, "DEP_ADD-1", 2))

	// Legit move happened.
	moved, err := s.GetNode(ctx, "DEP_ADD-2.1")
	require.NoError(t, err)
	assert.Equal(t, "real child", moved.Title)

	// The look-alike node is intact — same id, same content. Before the fix it
	// was cross-matched and rewritten to DEP_ADD-2.2 (wrong project, data loss).
	lookalike, err := s.GetNode(ctx, "DEPXADD-1.2")
	require.NoError(t, err, "DEPXADD-1.2 must survive a DEP_ADD renumber")
	assert.Equal(t, "DO NOT MOVE", lookalike.Title)
	_, err = s.GetNode(ctx, "DEPXADD-1")
	require.NoError(t, err, "DEPXADD-1 root untouched")
}

// TestRenumberSubtree_NamespaceFreeCheck_NoCrossPrefixFalsePositive ensures the
// target-namespace-free guard doesn't reject a legitimate renumber because a
// look-alike prefix's descendant matched the unescaped wildcard.
func TestRenumberSubtree_NamespaceFreeCheck_NoCrossPrefixFalsePositive(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	require.NoError(t, s.CreateNode(ctx, mkNode("AB_C-1", "", "AB_C", "to move")))
	// ABXC-2.1 would match an unescaped 'AB_C-2.%' target check and wrongly
	// report the namespace as taken.
	require.NoError(t, s.CreateNode(ctx, mkNode("ABXC-2", "", "ABXC", "lookalike")))
	require.NoError(t, s.CreateNode(ctx, likeTestChild("ABXC-2.1", "ABXC-2", "ABXC", "sib", 1)))

	require.NoError(t, s.RenumberSubtree(ctx, "AB_C-1", 2),
		"renumber to AB_C-2 must not be blocked by the look-alike ABXC-2.1")
	_, err := s.GetNode(ctx, "AB_C-2")
	require.NoError(t, err)
}
