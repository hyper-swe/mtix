// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// FR-MULTI-PROJECT AC-4 (the multi-hyphen sharp edge): a project prefix may
// itself contain hyphens (e.g. MTIX-DEV-OPS). The prefix/root boundary is the
// LAST dash before the first dot (model.splitID), and RenumberSubtree derives
// new ids from the node's stored project column + BuildID — never by re-parsing
// at the FIRST dash. These tests prove a multi-hyphen project round-trips
// create -> GetNode (show) -> GetTree (tree) -> RenumberSubtree with the full
// prefix intact, and that renumbering one project never cross-matches a
// look-alike prefix that shares a leading segment.
//
// Mapped to QUALITY-STANDARDS.md §3.6 scenario 19 via docs/traceability.json.
package sqlite_test

import (
	"context"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// mpChild builds a child fixture at an explicit depth and sibling seq.
func mpChild(id, parentID, project, title string, depth, seq int) *model.Node {
	n := mkNode(id, parentID, project, title)
	n.Depth = depth
	n.Seq = seq
	n.NodeType = model.NodeTypeForDepth(depth)
	n.ContentHash = n.ComputeHash()
	return n
}

// treeIDs returns the sorted ids of a GetTree result.
func treeIDs(nodes []*model.Node) []string {
	ids := make([]string, 0, len(nodes))
	for _, n := range nodes {
		ids = append(ids, n.ID)
	}
	sort.Strings(ids)
	return ids
}

// TestRenumberSubtree_MultiHyphenPrefix_PrefixIntact is the AC-4 KEY round-trip:
// a MTIX-DEV-OPS subtree is created, resolved by GetNode (show) and GetTree
// (tree), then renumbered at the root. Every rewritten id MUST keep the full
// multi-hyphen prefix and the stored project column MUST stay MTIX-DEV-OPS —
// proving the renumber never mis-parses the prefix at the first dash.
func TestRenumberSubtree_MultiHyphenPrefix_PrefixIntact(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()
	const proj = "MTIX-DEV-OPS"

	// create: a 3-level subtree under the multi-hyphen project.
	require.NoError(t, s.CreateNode(ctx, mkNode("MTIX-DEV-OPS-1", "", proj, "ops root")))
	require.NoError(t, s.CreateNode(ctx, mpChild("MTIX-DEV-OPS-1.1", "MTIX-DEV-OPS-1", proj, "child a", 1, 1)))
	require.NoError(t, s.CreateNode(ctx, mpChild("MTIX-DEV-OPS-1.1.1", "MTIX-DEV-OPS-1.1", proj, "grandchild", 2, 1)))
	require.NoError(t, s.CreateNode(ctx, mpChild("MTIX-DEV-OPS-1.2", "MTIX-DEV-OPS-1", proj, "child b", 1, 2)))

	// show: GetNode resolves the multi-hyphen id and reports the full project.
	root, err := s.GetNode(ctx, "MTIX-DEV-OPS-1")
	require.NoError(t, err)
	assert.Equal(t, proj, root.Project, "stored project keeps every hyphen, not just MTIX")

	// tree: GetTree resolves the whole subtree by the multi-hyphen root id.
	pre, err := s.GetTree(ctx, "MTIX-DEV-OPS-1", 10)
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"MTIX-DEV-OPS-1", "MTIX-DEV-OPS-1.1", "MTIX-DEV-OPS-1.1.1", "MTIX-DEV-OPS-1.2"},
		treeIDs(pre))

	// renumber: move the root 1 -> 3. The whole subtree is rewritten atomically.
	require.NoError(t, s.RenumberSubtree(ctx, "MTIX-DEV-OPS-1", 3))

	// Every new id keeps the full MTIX-DEV-OPS prefix; the old ids are gone.
	post, err := s.GetTree(ctx, "MTIX-DEV-OPS-3", 10)
	require.NoError(t, err)
	assert.Equal(t,
		[]string{"MTIX-DEV-OPS-3", "MTIX-DEV-OPS-3.1", "MTIX-DEV-OPS-3.1.1", "MTIX-DEV-OPS-3.2"},
		treeIDs(post),
		"renumber must keep the multi-hyphen prefix on every descendant id")

	for _, n := range post {
		assert.Equal(t, proj, n.Project,
			"node %s must stay in project %s after renumber (no mis-parse to MTIX)", n.ID, proj)
	}

	_, err = s.GetNode(ctx, "MTIX-DEV-OPS-1")
	require.ErrorIs(t, err, model.ErrNotFound, "old root id must no longer resolve")
}

// TestRenumberSubtree_MultiHyphenPrefix_NoCrossPrefixMatch proves that
// renumbering a node in MTIX-DEV-OPS never touches look-alike projects that
// share a leading segment — MTIX (one hyphen) and MTIX-DEV (two hyphens). A
// first-dash mis-parse, or an over-broad LIKE, would corrupt these; they MUST
// remain byte-for-byte intact.
func TestRenumberSubtree_MultiHyphenPrefix_NoCrossPrefixMatch(t *testing.T) {
	s := newUIDTestStore(t)
	ctx := context.Background()

	// Target project (renumbered below).
	require.NoError(t, s.CreateNode(ctx, mkNode("MTIX-DEV-OPS-1", "", "MTIX-DEV-OPS", "ops root")))
	require.NoError(t, s.CreateNode(ctx, mpChild("MTIX-DEV-OPS-1.1", "MTIX-DEV-OPS-1", "MTIX-DEV-OPS", "ops child", 1, 1)))

	// Look-alike A: prefix MTIX (shares the leading "MTIX-" segment).
	require.NoError(t, s.CreateNode(ctx, mkNode("MTIX-1", "", "MTIX", "primary root")))
	require.NoError(t, s.CreateNode(ctx, mpChild("MTIX-1.1", "MTIX-1", "MTIX", "DO NOT MOVE A", 1, 1)))

	// Look-alike B: prefix MTIX-DEV (shares "MTIX-DEV-", one hyphen short).
	require.NoError(t, s.CreateNode(ctx, mkNode("MTIX-DEV-1", "", "MTIX-DEV", "dev root")))
	require.NoError(t, s.CreateNode(ctx, mpChild("MTIX-DEV-1.1", "MTIX-DEV-1", "MTIX-DEV", "DO NOT MOVE B", 1, 1)))

	// Renumber only the MTIX-DEV-OPS root.
	require.NoError(t, s.RenumberSubtree(ctx, "MTIX-DEV-OPS-1", 2))

	// The legit move happened.
	moved, err := s.GetNode(ctx, "MTIX-DEV-OPS-2.1")
	require.NoError(t, err)
	assert.Equal(t, "ops child", moved.Title)

	// Both look-alikes are completely untouched — same ids, same content, same project.
	for _, lk := range []struct{ id, title, project string }{
		{"MTIX-1", "primary root", "MTIX"},
		{"MTIX-1.1", "DO NOT MOVE A", "MTIX"},
		{"MTIX-DEV-1", "dev root", "MTIX-DEV"},
		{"MTIX-DEV-1.1", "DO NOT MOVE B", "MTIX-DEV"},
	} {
		n, getErr := s.GetNode(ctx, lk.id)
		require.NoErrorf(t, getErr, "%s must survive a MTIX-DEV-OPS renumber", lk.id)
		assert.Equal(t, lk.title, n.Title, "%s content untouched", lk.id)
		assert.Equal(t, lk.project, n.Project, "%s project untouched", lk.id)
	}
}
