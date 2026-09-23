// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-95.18 regressions (review F-61): undelete restores only the
// descendants that the same delete removed. Every delete in these tests runs
// under one frozen store clock and, unless a row says otherwise, one author,
// so a child deleted on its own and its parent's later cascade carry the SAME
// deleted_at and deleted_by. A deleted_at/deleted_by heuristic cannot tell
// them apart; only the recorded cascade root can.
package sqlite_test

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// undelAuthor is the author of every delete unless a row says otherwise. The
// CLI records every delete as "cli", so same-author deletes are the norm.
const undelAuthor = "cli"

// undelDeleteTime is the frozen store clock: every delete lands in this second.
var undelDeleteTime = time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

// undelSpec describes one fixture node: its dot-notation id and whether it is
// a done leaf (progress 1.0) rather than an open node (progress 0.0).
type undelSpec struct {
	id   string
	done bool
}

// undelNode builds the model node for spec, deriving project, parent, depth
// and sibling number from the id.
func undelNode(spec undelSpec) *model.Node {
	project := model.ParseIDProject(spec.id)
	parent := model.ParseIDParent(spec.id)
	tail := spec.id[strings.LastIndexAny(spec.id, "-.")+1:]
	seq, err := strconv.Atoi(tail)
	if err != nil {
		panic("undelNode: bad id " + spec.id)
	}
	var n *model.Node
	if parent == "" {
		n = makeRootNode(spec.id, project, "node "+spec.id, undelDeleteTime)
	} else {
		n = makeChildNode(spec.id, parent, project, "node "+spec.id,
			strings.Count(spec.id, "."), seq, undelDeleteTime)
	}
	n.Seq = seq
	if spec.done {
		n.Status = model.StatusDone
		n.Progress = 1.0
	}
	return n
}

// seedUndeleteTree creates the fixture nodes, parents first, in a fresh store
// whose clock is frozen at undelDeleteTime.
func seedUndeleteTree(t *testing.T, specs ...undelSpec) *sqlite.Store {
	t.Helper()
	s := newTestStore(t)
	s.SetClock(func() time.Time { return undelDeleteTime })
	for _, spec := range specs {
		require.NoError(t, s.CreateNode(context.Background(), undelNode(spec)), "seed %s", spec.id)
	}
	return s
}

// openSpecs turns ids into open-node specs.
func openSpecs(ids ...string) []undelSpec {
	specs := make([]undelSpec, len(ids))
	for i, id := range ids {
		specs[i] = undelSpec{id: id}
	}
	return specs
}

// undelDeletion is one DeleteNode call made while building a scenario.
type undelDeletion struct {
	id      string
	cascade bool
}

// deleteMarks reads the raw deleted_at and deleted_by of id (deleted rows
// included).
func deleteMarks(t *testing.T, s *sqlite.Store, id string) (deletedAt, deletedBy sql.NullString) {
	t.Helper()
	err := s.QueryRow(context.Background(),
		"SELECT deleted_at, deleted_by FROM nodes WHERE id = ?", id).Scan(&deletedAt, &deletedBy)
	require.NoError(t, err, "read delete marks of %s", id)
	return deletedAt, deletedBy
}

// assertLiveAndDeleted checks that every id in live is live and every id in
// deleted is still soft-deleted.
func assertLiveAndDeleted(t *testing.T, s *sqlite.Store, live, deleted []string) {
	t.Helper()
	for _, id := range live {
		at, _ := deleteMarks(t, s, id)
		assert.False(t, at.Valid, "%s must be live", id)
	}
	for _, id := range deleted {
		at, _ := deleteMarks(t, s, id)
		assert.True(t, at.Valid, "%s must stay deleted", id)
	}
}

// markerRoot returns the cascade root recorded for id, and whether a marker
// row exists at all.
func markerRoot(t *testing.T, s *sqlite.Store, id string) (string, bool) {
	t.Helper()
	var root string
	err := s.QueryRow(context.Background(),
		"SELECT root_id FROM cascade_deletes WHERE node_id = ?", id).Scan(&root)
	if err == sql.ErrNoRows {
		return "", false
	}
	require.NoError(t, err, "read cascade marker of %s", id)
	return root, true
}

// markerCount returns the number of cascade_deletes rows.
func markerCount(t *testing.T, s *sqlite.Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.QueryRow(context.Background(),
		"SELECT COUNT(*) FROM cascade_deletes").Scan(&n))
	return n
}

// progressOf reads the raw stored progress of id.
func progressOf(t *testing.T, s *sqlite.Store, id string) float64 {
	t.Helper()
	var p float64
	require.NoError(t, s.QueryRow(context.Background(),
		"SELECT progress FROM nodes WHERE id = ?", id).Scan(&p), "read progress of %s", id)
	return p
}

// TestUndelete_IndependentlyDeletedChildStaysDeleted verifies that undeleting
// a cascade root leaves a descendant that was deleted on its own before the
// cascade deleted, even though both deletes carry the same deleted_at second
// and the same author (MTIX-95.18 story 1; review F-61). Red-first: the old
// undelete restored every descendant of the root.
func TestUndelete_IndependentlyDeletedChildStaysDeleted(t *testing.T) {
	tests := []struct {
		name        string
		nodes       []string
		independent []undelDeletion
		root        string
		wantLive    []string
		wantDeleted []string
	}{
		{
			name:        "child deleted alone before the parent cascade",
			nodes:       []string{"PROJ-1", "PROJ-1.1", "PROJ-1.2"},
			independent: []undelDeletion{{id: "PROJ-1.2"}},
			root:        "PROJ-1",
			wantLive:    []string{"PROJ-1", "PROJ-1.1"},
			wantDeleted: []string{"PROJ-1.2"},
		},
		{
			name:        "child cascade-deleted with its subtree before the parent cascade",
			nodes:       []string{"PROJ-1", "PROJ-1.1", "PROJ-1.1.1", "PROJ-1.2"},
			independent: []undelDeletion{{id: "PROJ-1.1", cascade: true}},
			root:        "PROJ-1",
			wantLive:    []string{"PROJ-1", "PROJ-1.2"},
			wantDeleted: []string{"PROJ-1.1", "PROJ-1.1.1"},
		},
		{
			name:        "grandchild deleted alone before the root cascade",
			nodes:       []string{"PROJ-1", "PROJ-1.1", "PROJ-1.1.1", "PROJ-1.1.2"},
			independent: []undelDeletion{{id: "PROJ-1.1.2"}},
			root:        "PROJ-1",
			wantLive:    []string{"PROJ-1", "PROJ-1.1", "PROJ-1.1.1"},
			wantDeleted: []string{"PROJ-1.1.2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := seedUndeleteTree(t, openSpecs(tt.nodes...)...)
			ctx := context.Background()
			for _, d := range tt.independent {
				require.NoError(t, s.DeleteNode(ctx, d.id, d.cascade, undelAuthor))
			}
			require.NoError(t, s.DeleteNode(ctx, tt.root, true, undelAuthor))

			// Precondition: the independent delete and the cascade are
			// indistinguishable by deleted_at and deleted_by.
			rootAt, rootBy := deleteMarks(t, s, tt.root)
			for _, id := range tt.wantDeleted {
				at, by := deleteMarks(t, s, id)
				require.Equal(t, rootAt, at, "%s deleted in the same second as %s", id, tt.root)
				require.Equal(t, rootBy, by, "%s deleted by the same author as %s", id, tt.root)
			}

			require.NoError(t, s.UndeleteNode(ctx, tt.root))

			assertLiveAndDeleted(t, s, tt.wantLive, tt.wantDeleted)
			for _, id := range tt.wantDeleted {
				at, by := deleteMarks(t, s, id)
				assert.Equal(t, rootAt, at, "%s keeps its own deleted_at", id)
				assert.Equal(t, rootBy, by, "%s keeps its own deleted_by", id)
			}
		})
	}
}

// TestUndelete_CascadeDeletedChildrenRestored verifies that undeleting a
// cascade root restores every descendant, at every depth, that the cascade
// removed; that done leaves keep their own progress; that the restored
// subtree's rollup and the parent's progress are recomputed (FR-5.7); and that
// the markers of the restored nodes are cleared (MTIX-95.18 story 3).
// Red-first: the old undelete also resurrected PROJ-1.1.3, which was deleted
// on its own before the cascade.
func TestUndelete_CascadeDeletedChildrenRestored(t *testing.T) {
	s := seedUndeleteTree(t,
		undelSpec{id: "PROJ-1"},
		undelSpec{id: "PROJ-1.1"},
		undelSpec{id: "PROJ-1.1.1", done: true},
		undelSpec{id: "PROJ-1.1.2"},
		undelSpec{id: "PROJ-1.1.2.1", done: true},
		undelSpec{id: "PROJ-1.1.2.2"},
		undelSpec{id: "PROJ-1.1.3"},
		undelSpec{id: "PROJ-1.2"},
	)
	ctx := context.Background()
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.1.3", false, undelAuthor))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.1", true, undelAuthor))
	require.InDelta(t, 0.0, progressOf(t, s, "PROJ-1"), 1e-9,
		"precondition: the parent has no live child with progress")

	require.NoError(t, s.UndeleteNode(ctx, "PROJ-1.1"))

	restored := []string{"PROJ-1.1", "PROJ-1.1.1", "PROJ-1.1.2", "PROJ-1.1.2.1", "PROJ-1.1.2.2"}
	assertLiveAndDeleted(t, s, restored, []string{"PROJ-1.1.3"})

	progress := []struct {
		id   string
		want float64
	}{
		{"PROJ-1.1.1", 1.0},   // done leaf keeps its own progress
		{"PROJ-1.1.2.1", 1.0}, // done leaf keeps its own progress
		{"PROJ-1.1.2", 0.5},   // (1.0 + 0.0) / 2
		{"PROJ-1.1", 0.75},    // (1.0 + 0.5) / 2; PROJ-1.1.3 stays out
		{"PROJ-1", 0.375},     // parent recomputed: (0.75 + 0.0) / 2
	}
	for _, p := range progress {
		assert.InDelta(t, p.want, progressOf(t, s, p.id), 1e-9, "progress of %s", p.id)
	}

	for _, id := range restored {
		_, ok := markerRoot(t, s, id)
		assert.False(t, ok, "marker of restored %s must be cleared", id)
	}
	root, ok := markerRoot(t, s, "PROJ-1.1.3")
	assert.True(t, ok, "the still-deleted node keeps its marker")
	assert.Equal(t, "PROJ-1.1.3", root)
}

// TestUndeleteNode_ChildRedeletedWhileRootDeleted_RecomputesRestoredParent
// verifies that restoring a node recomputes its parent even when that parent
// is itself restored by the same undelete. PROJ-1.2 is undeleted on its own
// under the deleted root and then deleted again; undeleting the root must
// leave it deleted and recompute the root's rollup from the child that came
// back (MTIX-95.18 story 3, FR-5.7).
func TestUndeleteNode_ChildRedeletedWhileRootDeleted_RecomputesRestoredParent(t *testing.T) {
	s := seedUndeleteTree(t,
		undelSpec{id: "PROJ-1"},
		undelSpec{id: "PROJ-1.1", done: true},
		undelSpec{id: "PROJ-1.2"},
	)
	ctx := context.Background()
	require.InDelta(t, 0.5, progressOf(t, s, "PROJ-1"), 1e-9, "precondition")
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", true, undelAuthor))
	require.NoError(t, s.UndeleteNode(ctx, "PROJ-1.2"))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.2", false, undelAuthor))

	require.NoError(t, s.UndeleteNode(ctx, "PROJ-1"))

	assertLiveAndDeleted(t, s, []string{"PROJ-1", "PROJ-1.1"}, []string{"PROJ-1.2"})
	assert.InDelta(t, 1.0, progressOf(t, s, "PROJ-1"), 1e-9,
		"the root's rollup counts only the restored PROJ-1.1")
}

// TestUndeleteNode_DescendantOfStillDeletedCascade_RestoresSameDeleteOnly
// verifies that undeleting X, removed by a still-deleted ancestor's cascade,
// restores X and the descendants that the same delete removed, and nothing
// that was deleted on its own. The ancestor and its other descendants stay
// deleted until the ancestor is undeleted (MTIX-95.18 story 4).
func TestUndeleteNode_DescendantOfStillDeletedCascade_RestoresSameDeleteOnly(t *testing.T) {
	s := seedUndeleteTree(t, openSpecs(
		"PROJ-1", "PROJ-1.1", "PROJ-1.1.1", "PROJ-1.1.1.1", "PROJ-1.1.2", "PROJ-1.2")...)
	ctx := context.Background()
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.1.2", false, undelAuthor))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", true, undelAuthor))

	steps := []struct {
		name        string
		undelete    string
		wantLive    []string
		wantDeleted []string
	}{
		{
			name:        "undelete the descendant under the deleted root",
			undelete:    "PROJ-1.1",
			wantLive:    []string{"PROJ-1.1", "PROJ-1.1.1", "PROJ-1.1.1.1"},
			wantDeleted: []string{"PROJ-1", "PROJ-1.2", "PROJ-1.1.2"},
		},
		{
			name:        "then undelete the root",
			undelete:    "PROJ-1",
			wantLive:    []string{"PROJ-1", "PROJ-1.2", "PROJ-1.1", "PROJ-1.1.1", "PROJ-1.1.1.1"},
			wantDeleted: []string{"PROJ-1.1.2"},
		},
	}
	// The steps run in order against one store: each builds on the last.
	for _, step := range steps {
		require.NoError(t, s.UndeleteNode(ctx, step.undelete), step.name)
		assertLiveAndDeleted(t, s, step.wantLive, step.wantDeleted)
	}
}

// legacyDelete soft-deletes ids the way a binary without cascade markers did:
// deleted_at and deleted_by only, no cascade_deletes row.
func legacyDelete(t *testing.T, s *sqlite.Store, at time.Time, by string, ids ...string) {
	t.Helper()
	stamp := at.UTC().Format(time.RFC3339)
	for _, id := range ids {
		res, err := s.WriteDB().ExecContext(context.Background(),
			"UPDATE nodes SET deleted_at = ?, deleted_by = ?, updated_at = ? WHERE id = ?",
			stamp, by, stamp, id)
		require.NoError(t, err, "legacy delete %s", id)
		n, err := res.RowsAffected()
		require.NoError(t, err)
		require.Equal(t, int64(1), n, "legacy delete %s", id)
	}
}

// TestUndeleteNode_UnmarkedPreUpgradeCascade_FallsBackToDeletedAtAndBy
// verifies the documented fallback for deletes recorded before cascade
// markers existed: a node with no marker restores the unmarked descendants
// whose deleted_at and deleted_by equal its own, and nothing else. A marked
// descendant, one deleted at another time or by another author, and a
// look-alike node of another project stay deleted (MTIX-95.18 story 5).
func TestUndeleteNode_UnmarkedPreUpgradeCascade_FallsBackToDeletedAtAndBy(t *testing.T) {
	tests := []struct {
		name        string
		undelete    string
		wantLive    []string
		wantDeleted []string
	}{
		{
			name:        "pre-upgrade cascade root",
			undelete:    "A_B-1",
			wantLive:    []string{"A_B-1", "A_B-1.1", "A_B-1.1.1"},
			wantDeleted: []string{"A_B-1.2", "A_B-1.3", "A_B-1.4", "AXB-1.1"},
		},
		{
			name:        "descendant removed by a pre-upgrade cascade",
			undelete:    "A_B-1.1",
			wantLive:    []string{"A_B-1.1", "A_B-1.1.1"},
			wantDeleted: []string{"A_B-1", "A_B-1.2", "A_B-1.3", "A_B-1.4", "AXB-1.1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := seedUndeleteTree(t, openSpecs("A_B-1", "A_B-1.1", "A_B-1.1.1",
				"A_B-1.2", "A_B-1.3", "A_B-1.4", "AXB-1", "AXB-1.1")...)
			ctx := context.Background()
			// Marked: deleted by this version, same second and author.
			require.NoError(t, s.DeleteNode(ctx, "A_B-1.4", false, undelAuthor))
			// Unmarked, pre-upgrade: the cascade itself, an earlier delete,
			// a same-second delete by another author, a look-alike project.
			legacyDelete(t, s, undelDeleteTime, undelAuthor, "A_B-1", "A_B-1.1", "A_B-1.1.1", "AXB-1.1")
			legacyDelete(t, s, undelDeleteTime.Add(-time.Hour), undelAuthor, "A_B-1.2")
			legacyDelete(t, s, undelDeleteTime, "another-agent", "A_B-1.3")
			_, marked := markerRoot(t, s, tt.undelete)
			require.False(t, marked, "precondition: the undelete target has no marker")

			require.NoError(t, s.UndeleteNode(ctx, tt.undelete))

			assertLiveAndDeleted(t, s, tt.wantLive, tt.wantDeleted)
		})
	}
}

// TestCascadeDelete_RecordsCascadeRoot_PerRemovedNode verifies that every
// delete records which delete removed each node it soft-deleted: the node it
// was called on names itself, and every live descendant a cascade removes
// names the cascade root. Nodes it did not remove (already deleted, live
// under a non-cascading delete, or in a look-alike project) get no marker
// from it (MTIX-95.18 story 2).
func TestCascadeDelete_RecordsCascadeRoot_PerRemovedNode(t *testing.T) {
	s := seedWildcardLookalikes(t)
	ctx := context.Background()
	s.SetClock(func() time.Time { return undelDeleteTime })
	require.NoError(t, s.CreateNode(ctx, undelNode(undelSpec{id: "A_B-1.2"})))
	require.NoError(t, s.CreateNode(ctx, undelNode(undelSpec{id: "A_B-10.1"})))
	// A stale marker on a live node (left by an older binary's undelete) is
	// overwritten by the delete that actually removes the node.
	_, err := s.WriteDB().ExecContext(ctx,
		"INSERT INTO cascade_deletes (node_id, root_id) VALUES (?, ?)", likeGrandchild, likeSibling)
	require.NoError(t, err)

	require.NoError(t, s.DeleteNode(ctx, "A_B-1.2", false, undelAuthor))
	require.NoError(t, s.DeleteNode(ctx, likeSibling, false, undelAuthor))
	require.NoError(t, s.DeleteNode(ctx, likeRoot, true, undelAuthor))

	tests := []struct {
		id       string
		wantRoot string // empty: no marker
	}{
		{likeRoot, likeRoot},
		{likeChild, likeRoot},
		{likeGrandchild, likeRoot},
		{"A_B-1.2", "A_B-1.2"},
		{likeSibling, likeSibling},
		{"A_B-10.1", ""},
		{likeLookalikeRoot, ""},
		{likeLookalikeChild, ""},
	}
	for _, tt := range tests {
		t.Run(tt.id, func(t *testing.T) {
			root, ok := markerRoot(t, s, tt.id)
			assert.Equal(t, tt.wantRoot != "", ok, "marker presence")
			assert.Equal(t, tt.wantRoot, root)
		})
	}
}

// TestCascadeDelete_FailureMidCascade_RollsBackMarkers verifies that the
// markers are written in the delete's own transaction: when the cascade fails
// part-way, no marker and no soft-delete survives (MTIX-95.18 story 2).
func TestCascadeDelete_FailureMidCascade_RollsBackMarkers(t *testing.T) {
	s := seedUndeleteTree(t, openSpecs("PROJ-1", "PROJ-1.1", "PROJ-1.1.1")...)
	ctx := context.Background()
	_, err := s.WriteDB().ExecContext(ctx, `
		CREATE TRIGGER fail_cascade BEFORE UPDATE OF deleted_at ON nodes
		WHEN old.id = 'PROJ-1.1.1' AND new.deleted_at IS NOT NULL
		BEGIN SELECT RAISE(ABORT, 'injected cascade failure'); END`)
	require.NoError(t, err)

	err = s.DeleteNode(ctx, "PROJ-1", true, undelAuthor)
	require.Error(t, err)

	assert.Equal(t, 0, markerCount(t, s), "no marker outlives the failed delete")
	assertLiveAndDeleted(t, s, []string{"PROJ-1", "PROJ-1.1", "PROJ-1.1.1"}, nil)
}

// seedMarkedCascade builds PROJ-1 > PROJ-1.1 > {PROJ-1.1.1, PROJ-1.1.2},
// deletes PROJ-1.1.2 on its own and then cascade-deletes PROJ-1.1, all in one
// second, leaving three markers.
func seedMarkedCascade(t *testing.T) *sqlite.Store {
	t.Helper()
	s := seedUndeleteTree(t, openSpecs("PROJ-1", "PROJ-1.1", "PROJ-1.1.1", "PROJ-1.1.2")...)
	ctx := context.Background()
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.1.2", false, undelAuthor))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1.1", true, undelAuthor))
	require.Equal(t, 3, markerCount(t, s), "precondition: three markers")
	return s
}

// TestCascadeMarkers_IDRewrites_FollowTheNodes verifies that the markers
// follow a node through every path that rewrites node ids (renumber and the
// reconcile rename), through the ON UPDATE CASCADE foreign keys, so undelete
// still restores exactly the cascade afterwards (MTIX-95.18).
func TestCascadeMarkers_IDRewrites_FollowTheNodes(t *testing.T) {
	tests := []struct {
		name        string
		rewrite     func(t *testing.T, s *sqlite.Store)
		undelete    string
		wantLive    []string
		wantDeleted []string
	}{
		{
			name: "renumber of the live parent",
			rewrite: func(t *testing.T, s *sqlite.Store) {
				require.NoError(t, s.RenumberSubtree(context.Background(), "PROJ-1", 7))
			},
			undelete:    "PROJ-7.1",
			wantLive:    []string{"PROJ-7.1", "PROJ-7.1.1"},
			wantDeleted: []string{"PROJ-7.1.2"},
		},
		{
			name: "reconcile rename to a new prefix",
			rewrite: func(t *testing.T, s *sqlite.Store) {
				_, err := sqlite.RenameTo(context.Background(), s, t.TempDir(), "NEWP")
				require.NoError(t, err)
			},
			undelete:    "NEWP-1.1",
			wantLive:    []string{"NEWP-1.1", "NEWP-1.1.1"},
			wantDeleted: []string{"NEWP-1.1.2"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := seedMarkedCascade(t)
			tt.rewrite(t, s)
			root, ok := markerRoot(t, s, tt.wantLive[1])
			require.True(t, ok, "marker follows the renamed descendant")
			require.Equal(t, tt.undelete, root, "marker names the renamed root")

			require.NoError(t, s.UndeleteNode(context.Background(), tt.undelete))

			assertLiveAndDeleted(t, s, tt.wantLive, tt.wantDeleted)
		})
	}
}

// TestCascadeMarkers_DiscardLocal_RemovesMarkers verifies that the reconcile
// discard, which hard-deletes every node, also removes every marker through
// the ON DELETE CASCADE foreign keys (MTIX-95.18).
func TestCascadeMarkers_DiscardLocal_RemovesMarkers(t *testing.T) {
	s := seedMarkedCascade(t)

	require.NoError(t, sqlite.DiscardLocal(context.Background(), s, t.TempDir()))

	assert.Equal(t, 0, markerCount(t, s))
}

// TestCascadeProvenance_WriteFailure_RollsBackWholeOperation verifies the error
// paths of the provenance writes: when recording a cascade row, restoring a
// node, clearing its row or recomputing progress fails, DeleteNode and
// UndeleteNode return the error and nothing they changed survives
// (MTIX-95.18). Each failure is injected with a trigger.
func TestCascadeProvenance_WriteFailure_RollsBackWholeOperation(t *testing.T) {
	subtree := []string{"PROJ-1", "PROJ-1.1"}
	tests := []struct {
		name        string
		predelete   bool   // cascade-delete PROJ-1 before the trigger exists
		trigger     string // complete constant DDL of the failing trigger
		op          func(s *sqlite.Store) error
		wantErr     string
		wantDeleted bool
		wantMarkers int
	}{
		{
			name: "delete: recording the target's own row fails",
			trigger: `CREATE TRIGGER inject_failure BEFORE INSERT ON cascade_deletes
				WHEN new.node_id = new.root_id
				BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
			op: func(s *sqlite.Store) error {
				return s.DeleteNode(context.Background(), "PROJ-1", true, undelAuthor)
			},
			wantErr: "record cascade root of PROJ-1",
		},
		{
			name: "delete: recording a descendant's row fails",
			trigger: `CREATE TRIGGER inject_failure BEFORE INSERT ON cascade_deletes
				WHEN new.node_id <> new.root_id
				BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
			op: func(s *sqlite.Store) error {
				return s.DeleteNode(context.Background(), "PROJ-1", true, undelAuthor)
			},
			wantErr: "record cascade of PROJ-1",
		},
		{
			name:      "undelete: restoring a node fails",
			predelete: true,
			trigger: `CREATE TRIGGER inject_failure BEFORE UPDATE OF deleted_at ON nodes
				WHEN new.deleted_at IS NULL
				BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
			op: func(s *sqlite.Store) error {
				return s.UndeleteNode(context.Background(), "PROJ-1")
			},
			wantErr: "undelete node", wantDeleted: true, wantMarkers: 2,
		},
		{
			name:      "undelete: clearing a row fails",
			predelete: true,
			trigger: `CREATE TRIGGER inject_failure BEFORE DELETE ON cascade_deletes
				BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
			op: func(s *sqlite.Store) error {
				return s.UndeleteNode(context.Background(), "PROJ-1")
			},
			wantErr: "clear cascade row of", wantDeleted: true, wantMarkers: 2,
		},
		{
			name:      "undelete: recomputing progress fails",
			predelete: true,
			trigger: `CREATE TRIGGER inject_failure BEFORE UPDATE OF progress ON nodes
				BEGIN SELECT RAISE(ABORT, 'injected failure'); END`,
			op: func(s *sqlite.Store) error {
				return s.UndeleteNode(context.Background(), "PROJ-1")
			},
			wantErr: "recalculate progress of PROJ-1 after undelete", wantDeleted: true, wantMarkers: 2,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := seedUndeleteTree(t, undelSpec{id: "PROJ-1"}, undelSpec{id: "PROJ-1.1", done: true})
			ctx := context.Background()
			if tt.predelete {
				require.NoError(t, s.DeleteNode(ctx, "PROJ-1", true, undelAuthor))
			}
			_, err := s.WriteDB().ExecContext(ctx, tt.trigger)
			require.NoError(t, err)

			err = tt.op(s)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)

			if tt.wantDeleted {
				assertLiveAndDeleted(t, s, nil, subtree)
			} else {
				assertLiveAndDeleted(t, s, subtree, nil)
			}
			assert.Equal(t, tt.wantMarkers, markerCount(t, s), "markers unchanged by the failed operation")
		})
	}
}
