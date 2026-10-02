// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.13: the deletion state is a workflow value, a chosen
// status rolls up to the parents' progress (FR-5.7), and the chosen side's
// previous status and invalidation fields move with its status.
package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// nodeState is the workflow columns of one exported node.
type nodeState struct {
	Status, PreviousStatus, DeletedAt, DeletedBy, InvalidatedBy, InvalidationReason string
	Progress                                                                        float64
}

// exportedNode returns node id from s's export.
func wfNodeState(t *testing.T, s *sqlite.Store, id string) nodeState {
	t.Helper()
	for _, n := range exportOf(t, s).Nodes {
		if n.ID == id {
			return nodeState{n.Status, n.PreviousStatus, n.DeletedAt, n.DeletedBy, n.InvalidatedBy, n.InvalidationReason, n.Progress}
		}
	}
	require.FailNow(t, "node not exported: "+id)
	return nodeState{}
}

// TestImportReconcile_DeletionState_IsAWorkflowConflict verifies a
// teammate's delete over unchanged content, and a local delete a
// teammate's text edit would undo, are refused and settled by the flags.
func TestImportReconcile_DeletionState_IsAWorkflowConflict(t *testing.T) {
	remove := func(t *testing.T, s *sqlite.Store) {
		t.Helper()
		require.NoError(t, s.DeleteNode(context.Background(), "REC-1", false, "tester"))
	}
	tests := []struct {
		name         string
		local        workflowEdit
		teammate     workflowEdit
		editsText    bool
		theirsDelete bool // after "theirs" the task is deleted
	}{
		{"teammate delete, content unchanged", noEdit, remove, false, true},
		{"local delete undone by a text edit", remove, noEdit, true, false},
		{"local delete, content unchanged", remove, noEdit, false, false},
	}
	for _, tt := range tests {
		for _, choice := range []sqlite.WorkflowChoice{sqlite.WorkflowTheirs, sqlite.WorkflowOurs} {
			t.Run(tt.name+"/"+string(choice), func(t *testing.T) {
				local, _, file := workflowStores(t, workflowCase{
					name: tt.name, base: noEdit, local: tt.local, teammate: tt.teammate, teammateEditsText: tt.editsText,
				})
				before := storeSnapshotJSON(t, local)

				report, err := mergeWith(local, file, sqlite.WorkflowResolution{})
				require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
				assert.Equal(t, "deleted_at", report.WorkflowConflicts[0].Fields[0].Field)
				assert.Equal(t, before, storeSnapshotJSON(t, local), "a refusal writes nothing")

				_, err = mergeWith(local, file, sqlite.WorkflowResolution{Prefer: choice})
				require.NoError(t, err)
				deleted := wfNodeState(t, local, "REC-1").DeletedAt != ""
				wantDeleted := tt.theirsDelete == (choice == sqlite.WorkflowTheirs)
				assert.Equal(t, wantDeleted, deleted)
				// The deleter moves with the deletion state, and an undelete clears it.
				wantBy := ""
				if wantDeleted {
					wantBy = "tester"
				}
				assert.Equal(t, wantBy, wfNodeState(t, local, "REC-1").DeletedBy)
			})
		}
	}
}

// TestImportReconcile_ChosenStatus_RollsUpToTheParent verifies the chosen
// file's child statuses recalculate the parent's progress in the same
// import (FR-5.7): the teammate claims REC-1.1 and closes REC-1.2; the
// file's parent row is stale (0), so only the roll-up can fix it. Keeping
// the local statuses leaves the parent at 0.
func TestImportReconcile_ChosenStatus_RollsUpToTheParent(t *testing.T) {
	ctx := context.Background()
	for _, choice := range []sqlite.WorkflowChoice{sqlite.WorkflowTheirs, sqlite.WorkflowOurs} {
		t.Run(string(choice), func(t *testing.T) {
			local, teammate := newTestStore(t), newTestStore(t)
			uids := map[string]string{"REC-1": taskUID(t), "REC-1.1": taskUID(t), "REC-1.2": taskUID(t)}
			for _, s := range []*sqlite.Store{local, teammate} {
				createSameIDTask(t, s, "REC-1", "", 1, uids["REC-1"], "Parent")
				createSameIDTask(t, s, "REC-1.1", "REC-1", 1, uids["REC-1.1"], "First child")
				createSameIDTask(t, s, "REC-1.2", "REC-1", 2, uids["REC-1.2"], "Second child")
			}
			require.NoError(t, teammate.ClaimNode(ctx, "REC-1.1", "bob"))
			require.NoError(t, teammate.ClaimNode(ctx, "REC-1.2", "bob"))
			require.NoError(t, teammate.TransitionStatus(ctx, "REC-1.2", model.StatusDone, "done", "bob"))
			require.InDelta(t, 0.5, wfNodeState(t, teammate, "REC-1").Progress, 0.001, "the teammate's parent rolled up")
			file := exportOf(t, teammate)
			for i := range file.Nodes {
				if file.Nodes[i].ID == "REC-1" {
					file.Nodes[i].Progress = 0 // a stale parent row: only the roll-up can fix it
				}
			}
			file.Checksum = sqlite.RecomputeChecksumForTest(t, file)

			_, err := mergeWith(local, file, sqlite.WorkflowResolution{Prefer: choice})
			require.NoError(t, err)

			if choice == sqlite.WorkflowTheirs {
				assert.InDelta(t, 0.5, wfNodeState(t, local, "REC-1").Progress, 0.001, "the parent follows the chosen children")
				assert.Equal(t, "done", wfNodeState(t, local, "REC-1.2").Status)
			} else {
				assert.InDelta(t, 0.0, wfNodeState(t, local, "REC-1").Progress, 0.001)
				assert.Equal(t, "open", wfNodeState(t, local, "REC-1.2").Status)
			}
		})
	}
}

// blockedStores returns a local store and a teammate's store holding REC-1
// and its open blocker REC-2; the teammate's REC-1 was in progress (when
// claimed) and the blocker auto-blocked it, which records its previous
// status (FR-3.8).
func blockedStores(t *testing.T, claimed bool) (*sqlite.Store, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	uid, blocker := taskUID(t), taskUID(t)
	for _, s := range []*sqlite.Store{local, teammate} {
		createSameIDTask(t, s, "REC-1", "", 1, uid, "Shared task")
		createSameIDTask(t, s, "REC-2", "", 2, blocker, "Blocker")
	}
	if claimed {
		require.NoError(t, teammate.ClaimNode(ctx, "REC-1", "bob"))
	}
	require.NoError(t, teammate.AddDependency(ctx, &model.Dependency{
		FromID: "REC-2", ToID: "REC-1", DepType: model.DepTypeBlocks, CreatedAt: sameIDTime,
	}))
	return local, teammate
}

// TestImportReconcile_TheirsPath_TakesPreviousStatus verifies the file's
// previous status moves with its blocked status, and that keeping the
// local values leaves it alone.
func TestImportReconcile_TheirsPath_TakesPreviousStatus(t *testing.T) {
	tests := []struct {
		name    string
		claimed bool
		want    string
	}{
		{"blocked from in_progress", true, "in_progress"},
		{"blocked from open", false, "open"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, teammate := blockedStores(t, tt.claimed)
			require.Equal(t, "blocked", wfNodeState(t, teammate, "REC-1").Status, "fixture: the blocker blocked it")
			require.Equal(t, tt.want, wfNodeState(t, teammate, "REC-1").PreviousStatus, "fixture: the previous status")
			file := exportOf(t, teammate)

			_, err := mergeWith(local, file, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowOurs})
			require.NoError(t, err)
			assert.Empty(t, wfNodeState(t, local, "REC-1").PreviousStatus, "ours keeps the local previous status")

			_, err = mergeWith(local, file, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs})
			require.NoError(t, err)
			got := wfNodeState(t, local, "REC-1")
			assert.Equal(t, "blocked", got.Status)
			assert.Equal(t, tt.want, got.PreviousStatus, "the file's previous status moves with its status")
		})
	}
}

// TestImportReconcile_OlderFileTheirs_KeepsPreviousStatusAndInvalidation
// verifies a schema 1.0.0 file carries neither previous status nor
// invalidation, so choosing its values keeps the local ones.
func TestImportReconcile_OlderFileTheirs_KeepsPreviousStatusAndInvalidation(t *testing.T) {
	ctx := context.Background()
	teammate, local := blockedStores(t, true) // the local store is the blocked one
	_, err := local.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET invalidated_by = 'reviewer', invalidation_reason = 'stale' WHERE id = 'REC-1'`)
	require.NoError(t, err)
	require.Equal(t, "in_progress", wfNodeState(t, local, "REC-1").PreviousStatus, "fixture")
	file := exportOf(t, teammate)

	_, err = mergeWith(local, asSchemaV1(t, file), sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs})
	require.NoError(t, err)
	got := wfNodeState(t, local, "REC-1")
	assert.Equal(t, "open", got.Status, "the file's status applies")
	assert.Equal(t, "in_progress", got.PreviousStatus, "a 1.x file does not carry the previous status")
	assert.Equal(t, "reviewer", got.InvalidatedBy)
	assert.Equal(t, "stale", got.InvalidationReason)
}

// TestImportReconcile_RefusedReport_LeavesConflictsOutOfIdempotentNoOps
// verifies a conflicting task is not counted as an idempotent no-op.
func TestImportReconcile_RefusedReport_LeavesConflictsOutOfIdempotentNoOps(t *testing.T) {
	local, file := twoTaskStores(t)
	report, err := mergeWith(local, file, sqlite.WorkflowResolution{})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	assert.Zero(t, report.Idempotent, "both tasks differ, so neither is a no-op")
	assert.Contains(t, report.String(), "idempotent no-ops: 0")
}

// treeStores returns a local store and a teammate's store holding REC-1 with
// children REC-1.1 (done on both sides) and REC-1.2 (open), sharing uids.
func treeStores(t *testing.T) (*sqlite.Store, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	uids := []string{taskUID(t), taskUID(t), taskUID(t)}
	for _, s := range []*sqlite.Store{local, teammate} {
		createSameIDTask(t, s, "REC-1", "", 1, uids[0], "Parent")
		createSameIDTask(t, s, "REC-1.1", "REC-1", 1, uids[1], "First child")
		createSameIDTask(t, s, "REC-1.2", "REC-1", 2, uids[2], "Second child")
		require.NoError(t, s.ClaimNode(ctx, "REC-1.1", "ann"))
		require.NoError(t, s.TransitionStatus(ctx, "REC-1.1", model.StatusDone, "done", "ann"))
	}
	require.InDelta(t, 0.5, wfNodeState(t, local, "REC-1").Progress, 0.001, "fixture")
	return local, teammate
}

// staleParent returns file with REC-1's progress set to p, a stale row only
// the roll-up can correct.
func staleParent(t *testing.T, file *sqlite.ExportData, p float64) *sqlite.ExportData {
	t.Helper()
	for i := range file.Nodes {
		if file.Nodes[i].ID == "REC-1" {
			file.Nodes[i].Progress = p
		}
	}
	file.Checksum = sqlite.RecomputeChecksumForTest(t, file)
	return file
}

// TestImportReconcile_RollUp_StartsAtEveryWrittenParent verifies a parent
// the merge writes from the file is recomputed from its live children, the
// children's chosen side included: a parent reworded by the teammate whose
// children keep ours (X1), and a parent taken from the file whose child
// keeps ours (X5), both end at the local children's progress.
func TestImportReconcile_RollUp_StartsAtEveryWrittenParent(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		edit func(t *testing.T, teammate *sqlite.Store)
		res  sqlite.WorkflowResolution
	}{
		{"reworded parent, children ours", func(t *testing.T, s *sqlite.Store) {
			editTitle("Parent, reworded")(t, s)
			require.NoError(t, s.ClaimNode(ctx, "REC-1.2", "bob"))
			require.NoError(t, s.TransitionStatus(ctx, "REC-1.2", model.StatusDone, "done", "bob"))
		}, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowOurs}},
		{"parent theirs, child ours", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(ctx, "REC-1", "bob"))
			require.NoError(t, s.ClaimNode(ctx, "REC-1.2", "bob"))
			require.NoError(t, s.TransitionStatus(ctx, "REC-1.2", model.StatusDone, "done", "bob"))
		}, sqlite.WorkflowResolution{Theirs: []string{"REC-1"}, Ours: []string{"REC-1.2"}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, teammate := treeStores(t)
			tt.edit(t, teammate)
			file := exportOf(t, teammate)
			require.InDelta(t, 1.0, wfNodeState(t, teammate, "REC-1").Progress, 0.001, "fixture: the file's parent is complete")

			_, err := mergeWith(local, file, tt.res)
			require.NoError(t, err)
			assert.Equal(t, "open", wfNodeState(t, local, "REC-1.2").Status, "the child kept ours")
			assert.InDelta(t, 0.5, wfNodeState(t, local, "REC-1").Progress, 0.001, "the parent follows its live children")
		})
	}
}

// TestImportReconcile_RollUp_CancelAndDeleteMoveTheParent verifies a chosen
// cancel (the child's own progress stays 0, so only its status moves the
// parent) and a chosen deletion roll up to the parent (FR-5.4, FR-5.7).
func TestImportReconcile_RollUp_CancelAndDeleteMoveTheParent(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name string
		do   func(t *testing.T, s *sqlite.Store)
	}{
		{"cancelled child", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.CancelNode(ctx, "REC-1.2", "dropped", "bob", false))
		}},
		{"deleted child", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.DeleteNode(ctx, "REC-1.2", false, "bob"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, teammate := treeStores(t)
			tt.do(t, teammate)
			require.InDelta(t, 1.0, wfNodeState(t, teammate, "REC-1").Progress, 0.001, "fixture")
			file := staleParent(t, exportOf(t, teammate), 0.5)

			_, err := mergeWith(local, file, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs})
			require.NoError(t, err)
			assert.InDelta(t, 1.0, wfNodeState(t, local, "REC-1").Progress, 0.001, "the parent excludes the cancelled or deleted child")
		})
	}
}

// TestImportReconcile_RollUp_ParentWrittenFromFileFollowsUntouchedChildren
// verifies a parent the merge rewrites from the file (a text edit) is
// recomputed from its live children even when no child changed: the file's
// stale parent progress must not stand.
func TestImportReconcile_RollUp_ParentWrittenFromFileFollowsUntouchedChildren(t *testing.T) {
	local, teammate := treeStores(t)
	editTitle("Parent, reworded")(t, teammate)
	file := exportOf(t, teammate)
	// The children are identical on both sides (same activity), so the merge
	// leaves them alone and only the parent is written.
	held := exportOf(t, local)
	for i := range file.Nodes {
		for _, l := range held.Nodes {
			if l.ID == file.Nodes[i].ID && l.ID != "REC-1" {
				file.Nodes[i].Activity, file.Nodes[i].Annotations = l.Activity, l.Annotations
			}
		}
	}
	file = staleParent(t, file, 0.9)

	report, err := mergeWith(local, file, sqlite.WorkflowResolution{})
	require.NoError(t, err)
	assert.Equal(t, 1, report.Idempotent-2, "fixture: both children are untouched")
	assert.InDelta(t, 0.5, wfNodeState(t, local, "REC-1").Progress, 0.001)
}
