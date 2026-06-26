// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// seedRenumberTree builds a deep tree rooted at RNB-1 used by the renumber
// tests. Shape (depth in parens):
//
//	RNB-1 (0)
//	└── RNB-1.4 (1)            <- the node we renumber
//	    ├── RNB-1.4.1 (2)
//	    │   └── RNB-1.4.1.1 (3)
//	    │       └── RNB-1.4.1.1.1 (4)   <- deep nesting
//	    └── RNB-1.4.2 (2)
//	RNB-1.7 (1)               <- an unrelated sibling (must be untouched)
func seedRenumberTree(t *testing.T, s *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	nodes := []struct {
		id, parent string
		depth, seq int
	}{
		{"RNB-1", "", 0, 1},
		{"RNB-1.4", "RNB-1", 1, 4},
		{"RNB-1.4.1", "RNB-1.4", 2, 1},
		{"RNB-1.4.1.1", "RNB-1.4.1", 3, 1},
		{"RNB-1.4.1.1.1", "RNB-1.4.1.1", 4, 1},
		{"RNB-1.4.2", "RNB-1.4", 2, 2},
		{"RNB-1.7", "RNB-1", 1, 7},
	}
	for _, n := range nodes {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: n.id, ParentID: n.parent, Project: "RNB",
			Depth: n.depth, Seq: n.seq, Title: "T " + n.id,
			Status: model.StatusOpen, Priority: model.PriorityMedium,
			Weight: 1.0, NodeType: model.NodeTypeIssue,
			ContentHash: "h-" + n.id, CreatedAt: now, UpdatedAt: now,
		}), "seed %s", n.id)
	}
}

func uidOf(t *testing.T, s *sqlite.Store, displayPath string) string {
	t.Helper()
	uid, err := s.ResolveUIDByDisplayPath(context.Background(), displayPath)
	require.NoError(t, err)
	require.NotEmpty(t, uid)
	return uid
}

func countEvents(t *testing.T, s *sqlite.Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.ReadDB().QueryRowContext(
		context.Background(), `SELECT COUNT(*) FROM sync_events`).Scan(&n))
	return n
}

// TestRenumberSubtree_HappyPath_RewritesNodeAndAllDescendants is the core
// scenario: renumbering RNB-1.4 -> RNB-1.5 moves the node and recomputes the
// display path of the ENTIRE subtree at every depth (ADR-003 §5, F-2).
func TestRenumberSubtree_HappyPath_RewritesNodeAndAllDescendants(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 5))

	// Old paths gone, new paths present, at every depth.
	for _, gone := range []string{"RNB-1.4", "RNB-1.4.1", "RNB-1.4.1.1", "RNB-1.4.1.1.1", "RNB-1.4.2"} {
		_, err := s.GetNode(ctx, gone)
		require.ErrorIs(t, err, model.ErrNotFound, "old path %s must be gone", gone)
	}
	for _, want := range []string{"RNB-1.5", "RNB-1.5.1", "RNB-1.5.1.1", "RNB-1.5.1.1.1", "RNB-1.5.2"} {
		_, err := s.GetNode(ctx, want)
		require.NoError(t, err, "new path %s must exist", want)
	}
}

// TestRenumberSubtree_FixesParentIDsAndSeqAndDepth verifies the structural
// columns: the renumbered node's seq changes, descendants keep their seq/depth,
// and every child's parent_id is rewritten consistently (FK-safe, ADR-003 §5).
func TestRenumberSubtree_FixesParentIDsAndSeqAndDepth(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 5))

	moved, err := s.GetNode(ctx, "RNB-1.5")
	require.NoError(t, err)
	assert.Equal(t, 5, moved.Seq, "renumbered node seq updated")
	assert.Equal(t, "RNB-1", moved.ParentID, "renumbered node keeps its parent")
	assert.Equal(t, 1, moved.Depth, "renumbered node keeps its depth")

	child, err := s.GetNode(ctx, "RNB-1.5.1")
	require.NoError(t, err)
	assert.Equal(t, "RNB-1.5", child.ParentID, "child parent_id rewritten")
	assert.Equal(t, 1, child.Seq, "child seq unchanged")
	assert.Equal(t, 2, child.Depth, "child depth unchanged")

	deep, err := s.GetNode(ctx, "RNB-1.5.1.1.1")
	require.NoError(t, err)
	assert.Equal(t, "RNB-1.5.1.1", deep.ParentID, "deep parent_id rewritten")
	assert.Equal(t, 4, deep.Depth, "deep depth unchanged")

	// GetTree must reconstruct cleanly: root + 4 descendants.
	tree, err := s.GetTree(ctx, "RNB-1.5", 100)
	require.NoError(t, err)
	assert.Len(t, tree, 5, "subtree intact: node + 4 descendants")
}

// TestRenumberSubtree_PreservesStableUIDs verifies the renamed node and every
// descendant keep their durable uid (ADR-003 §2/§5): the surface number moved
// but the internal identity did not.
func TestRenumberSubtree_PreservesStableUIDs(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	before := map[string]string{
		"RNB-1.4":         uidOf(t, s, "RNB-1.4"),
		"RNB-1.4.1":       uidOf(t, s, "RNB-1.4.1"),
		"RNB-1.4.1.1.1":   uidOf(t, s, "RNB-1.4.1.1.1"),
	}

	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 5))

	after := map[string]string{
		"RNB-1.4":       uidOf(t, s, "RNB-1.5"),
		"RNB-1.4.1":     uidOf(t, s, "RNB-1.5.1"),
		"RNB-1.4.1.1.1": uidOf(t, s, "RNB-1.5.1.1.1"),
	}
	for old, uid := range before {
		assert.Equal(t, uid, after[old], "uid for %s must survive renumber", old)
	}
}

// TestRenumberSubtree_ReferenceSurvivesViaUID is the reference-resolution
// guarantee (ADR-003 §5): a caller who recorded the uid before the renumber
// resolves the new display path afterward — the reference survived.
func TestRenumberSubtree_ReferenceSurvivesViaUID(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	uid := uidOf(t, s, "RNB-1.4.1")

	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 5))

	got, err := s.ResolveDisplayPathByUID(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "RNB-1.5.1", got, "uid now resolves to the renumbered path")
}

// TestRenumberSubtree_EmitsNoSyncEvents verifies the 30.6 guarantee: because
// events key on uid, a display-path renumber rewrites the nodes table only and
// touches ZERO sync events (ADR-003 §3, §5).
func TestRenumberSubtree_EmitsNoSyncEvents(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	before := countEvents(t, s)
	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 5))
	assert.Equal(t, before, countEvents(t, s), "renumber must emit no sync events")
}

// TestRenumberSubtree_RewritesDependencyRefs verifies FK-referencing rows in
// the dependencies table follow the renumber, so no dependency dangles or is
// dropped (FK-safe, ADR-003 §5).
func TestRenumberSubtree_RewritesDependencyRefs(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// RNB-1.7 blocks RNB-1.4.1 (one endpoint inside the renumbered subtree).
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "RNB-1.7", ToID: "RNB-1.4.1",
		DepType: model.DepTypeBlocks, CreatedAt: now, CreatedBy: "pm",
	}))

	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 5))

	blockers, err := s.GetBlockers(ctx, "RNB-1.5.1")
	require.NoError(t, err)
	require.Len(t, blockers, 1, "dependency must follow the renumbered node")
	assert.Equal(t, "RNB-1.7", blockers[0].FromID)
	assert.Equal(t, "RNB-1.5.1", blockers[0].ToID)
}

// TestRenumberSubtree_Idempotent: renumbering to the node's current number is a
// clean no-op (ADR-003 §5 — resolution never ambiguous, nothing changes).
func TestRenumberSubtree_Idempotent(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	before := countEvents(t, s)
	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 4), "renumber to same seq is a no-op")

	_, err := s.GetNode(ctx, "RNB-1.4")
	require.NoError(t, err, "node still at its original path")
	assert.Equal(t, before, countEvents(t, s))

	// Applying twice with the same target is also idempotent.
	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 5))
	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.5", 5))
	_, err = s.GetNode(ctx, "RNB-1.5")
	require.NoError(t, err)
}

// TestRenumberSubtree_RejectsTakenSiblingNumber: renumbering to an already-used
// sibling number is rejected cleanly with ErrAlreadyExists and changes nothing
// (ADR-003 §5 — a number must never bind to two nodes).
func TestRenumberSubtree_RejectsTakenSiblingNumber(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	err := s.RenumberSubtree(ctx, "RNB-1.4", 7) // RNB-1.7 already exists
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrAlreadyExists)

	// Nothing moved: both nodes still at their original paths.
	_, err = s.GetNode(ctx, "RNB-1.4")
	require.NoError(t, err)
	_, err = s.GetNode(ctx, "RNB-1.7")
	require.NoError(t, err)
}

// TestRenumberSubtree_RejectsTakenNumberHeldByDescendantPath guards against the
// transient/ambiguous-resolution corner: a sibling number whose target subtree
// namespace is partially occupied must be rejected before any mutation.
func TestRenumberSubtree_RejectsTakenNumberHeldByLiveSibling(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	// Add a sibling RNB-1.5 with its own child, then try to move RNB-1.4 -> 5.
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "RNB-1.5", ParentID: "RNB-1", Project: "RNB",
		Depth: 1, Seq: 5, Title: "occupant", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeIssue,
		ContentHash: "h", CreatedAt: now, UpdatedAt: now,
	}))

	err := s.RenumberSubtree(ctx, "RNB-1.4", 5)
	require.ErrorIs(t, err, model.ErrAlreadyExists)

	// The occupant and the source are both untouched.
	_, err = s.GetNode(ctx, "RNB-1.5")
	require.NoError(t, err)
	_, err = s.GetNode(ctx, "RNB-1.4.1.1.1")
	require.NoError(t, err)
}

// TestRenumberSubtree_RejectsNumberHeldBySoftDeletedNode: a soft-deleted node
// still holds its primary-key id, so its number is taken — renumbering onto it
// must be rejected so the id is never reused while the tombstone exists
// (ADR-003 §5 — a number must never bind to two rows).
func TestRenumberSubtree_RejectsNumberHeldBySoftDeletedNode(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	// Soft-delete the live sibling RNB-1.7, then try to move RNB-1.4 -> 7.
	require.NoError(t, s.DeleteNode(ctx, "RNB-1.7", false, "pm"))

	err := s.RenumberSubtree(ctx, "RNB-1.4", 7)
	require.ErrorIs(t, err, model.ErrAlreadyExists)

	// Source untouched.
	_, err = s.GetNode(ctx, "RNB-1.4")
	require.NoError(t, err)
}

// TestRenumberSubtree_RootNode verifies a depth-0 (root) node renumbers, where
// BuildID uses the PREFIX-N form rather than parent.N.
func TestRenumberSubtree_RootNode(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1", 9))

	_, err := s.GetNode(ctx, "RNB-1")
	require.ErrorIs(t, err, model.ErrNotFound)

	moved, err := s.GetNode(ctx, "RNB-9")
	require.NoError(t, err)
	assert.Equal(t, "", moved.ParentID)
	assert.Equal(t, 9, moved.Seq)

	// Whole subtree shifted to the new root prefix.
	deep, err := s.GetNode(ctx, "RNB-9.4.1.1.1")
	require.NoError(t, err)
	assert.Equal(t, "RNB-9.4.1.1", deep.ParentID)
}

// TestRenumberSubtree_NotFound: renumbering a missing node is a clean error.
func TestRenumberSubtree_NotFound(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	err := s.RenumberSubtree(ctx, "RNB-1.99", 3)
	require.ErrorIs(t, err, model.ErrNotFound)
}

// TestRenumberSubtree_RejectsInvalidSeq: a non-positive target number is
// invalid input (display numbers are 1-based, ADR-003 §4).
func TestRenumberSubtree_RejectsInvalidSeq(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	for _, bad := range []int{0, -1} {
		err := s.RenumberSubtree(ctx, "RNB-1.4", bad)
		require.ErrorIs(t, err, model.ErrInvalidInput, "seq %d must be rejected", bad)
	}
}

// TestRenumberSubtree_RejectsEmptyID: empty id is invalid input.
func TestRenumberSubtree_RejectsEmptyID(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	err := s.RenumberSubtree(ctx, "", 3)
	require.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestRenumberSubtree_ConcurrentReaderSeesAllOldOrAllNew is the atomicity/
// isolation corner (ADR-003 §5, F-2): a reader running concurrently with the
// renumber must observe the subtree entirely at its old paths OR entirely at its
// new paths, never a mix (no parent moved while a descendant has not).
func TestRenumberSubtree_ConcurrentReaderSeesAllOldOrAllNew(t *testing.T) {
	s := newTestStore(t)
	seedRenumberTree(t, s)
	ctx := context.Background()

	var wg sync.WaitGroup
	stop := make(chan struct{})
	var mixed, sawOld, sawNew int32

	// Reader: in a SINGLE read transaction (one consistent WAL snapshot),
	// observe both the parent and a deep descendant. The atomicity guarantee
	// (ADR-003 §5, F-2) is that within one snapshot the whole subtree is at its
	// old paths OR all at its new paths — never a mix. Reading both rows under
	// the same snapshot is what actually exercises that guarantee (four
	// independent reads would each see a different committed snapshot and tell
	// us nothing about atomicity).
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
			}
			parentOld, deepOld, parentNew, deepNew := snapshotSubtree(t, s)
			allOld := parentOld && deepOld && !parentNew && !deepNew
			allNew := parentNew && deepNew && !parentOld && !deepOld
			switch {
			case allOld:
				atomic.StoreInt32(&sawOld, 1)
			case allNew:
				atomic.StoreInt32(&sawNew, 1)
			default:
				atomic.StoreInt32(&mixed, 1)
			}
		}
	}()

	require.NoError(t, s.RenumberSubtree(ctx, "RNB-1.4", 5))
	// Let the reader observe the committed new generation, then stop.
	time.AfterFunc(20*time.Millisecond, func() { close(stop) })
	wg.Wait()

	assert.Zero(t, atomic.LoadInt32(&mixed), "concurrent reader observed a torn (mixed-generation) subtree")
	assert.Equal(t, int32(1), atomic.LoadInt32(&sawNew), "reader must have observed the renumbered subtree")
}

// snapshotSubtree reports the presence of the parent and a deep descendant at
// both their old and new paths, read within ONE read transaction so all four
// observations come from a single consistent WAL snapshot.
func snapshotSubtree(t *testing.T, s *sqlite.Store) (parentOld, deepOld, parentNew, deepNew bool) {
	t.Helper()
	tx, err := s.ReadDB().BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	exists := func(id string) bool {
		var one int
		err := tx.QueryRowContext(context.Background(),
			`SELECT 1 FROM nodes WHERE id = ? AND deleted_at IS NULL`, id).Scan(&one)
		if errors.Is(err, sql.ErrNoRows) {
			return false
		}
		require.NoError(t, err)
		return true
	}
	return exists("RNB-1.4"), exists("RNB-1.4.1.1.1"),
		exists("RNB-1.5"), exists("RNB-1.5.1.1.1")
}

// TestRenumberSubtree_WideSubtree exercises a node with many direct children to
// confirm the single-statement rewrite scales past the tiny seeded shape.
func TestRenumberSubtree_WideSubtree(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "WID-2", Project: "WID", Depth: 0, Seq: 2, Title: "root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h", CreatedAt: now, UpdatedAt: now,
	}))
	const n = 25
	for i := 1; i <= n; i++ {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: "WID-2." + strconv.Itoa(i), ParentID: "WID-2", Project: "WID",
			Depth: 1, Seq: i, Title: "c", Status: model.StatusOpen,
			Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeIssue,
			ContentHash: "h", CreatedAt: now, UpdatedAt: now,
		}))
	}

	require.NoError(t, s.RenumberSubtree(ctx, "WID-2", 3))

	tree, err := s.GetTree(ctx, "WID-3", 100)
	require.NoError(t, err)
	assert.Len(t, tree, n+1)
	for _, node := range tree {
		assert.True(t, strings.HasPrefix(node.ID, "WID-3"), "every node under new prefix: %s", node.ID)
	}
}
