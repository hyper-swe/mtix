// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Round-2 tests for MTIX-95.31.4 (FR-15.2i, FR-7.8): a merge keeps a local
// value that a stale copy of the node leaves empty; a local uid the file
// holds under another id is the same task moved there, not a different
// task; the same id with the same creation time is the same task even when
// the uids differ (clones upgraded from before uids were shared); and the
// numbering of renumbered local tasks. Written red-first against round 1.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// exportOf returns s's export.
func exportOf(t *testing.T, s *sqlite.Store) *sqlite.ExportData {
	t.Helper()
	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	return data
}

// asSchemaV1 turns an export into what mtix 0.5.3 wrote: schema 1.0.0,
// without annotations and activity, with a valid checksum.
func asSchemaV1(t *testing.T, data *sqlite.ExportData) *sqlite.ExportData {
	t.Helper()
	data.SchemaVersion = "1.0.0"
	for i := range data.Nodes {
		data.Nodes[i].Annotations, data.Nodes[i].Activity = nil, nil
	}
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	return data
}

// mergeFile runs the merge mtix import --mode merge runs, without
// confirmation, and requires it to apply.
func mergeFile(t *testing.T, s *sqlite.Store, data *sqlite.ExportData) *sqlite.ImportReconcileReport {
	t.Helper()
	report, _, err := s.ImportReconcile(context.Background(), data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err)
	require.True(t, report.Applied)
	return report
}

// createPlainTask creates root task id with uid, an empty description, a
// real content hash and the given update time, in s.
func createPlainTask(t *testing.T, s *sqlite.Store, id string, seq int, uid string, updated time.Time) {
	t.Helper()
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: id, Project: "REC", Depth: 0, Seq: seq, Title: "Shared task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeForDepth(0), ContentHash: model.ComputeContentHash("Shared task", "", "", "", nil),
		UID: uid, CreatedAt: sameIDTime, UpdatedAt: updated,
	}))
}

// setUpdatedAt sets node id's updated_at in s, as a write at that time
// would.
func setUpdatedAt(t *testing.T, s *sqlite.Store, id string, at time.Time) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET updated_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339), id)
	require.NoError(t, err)
}

// TestImportReconcile_MergeOfStaleCopy_KeepsFieldTheCopyBlanks verifies a
// merge keeps a local non-empty value that the file's copy of the node
// leaves empty when that copy is not current (it lacks a local activity
// entry, is older, or carries no activity at all), with the local status
// when the value belongs to it; the stale copy's other changes apply and
// the content hash matches the merged content.
func TestImportReconcile_MergeOfStaleCopy_KeepsFieldTheCopyBlanks(t *testing.T) {
	later := sameIDTime.Add(time.Hour)
	describe := func(t *testing.T, s *sqlite.Store) {
		d := "Local description"
		require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{Description: &d}))
		setUpdatedAt(t, s, "REC-1", later)
	}
	tests := []struct {
		name     string
		local    func(t *testing.T, s *sqlite.Store) // after the teammate's copy is taken
		teammate func(t *testing.T, s *sqlite.Store)
		v1       bool
		check    func(t *testing.T, n *model.Node)
	}{
		{"older 2.0.0 copy leaves the description empty", describe, nil, false, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Local description", n.Description)
		}},
		{"0.5.3 copy leaves the description empty", describe, nil, true, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Local description", n.Description)
		}},
		{"newer copy without the local close reopens the task", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(context.Background(), "REC-1", "agent-a"))
			require.NoError(t, s.TransitionStatus(context.Background(), "REC-1", model.StatusDone, "done", "agent-a"))
			setUpdatedAt(t, s, "REC-1", later) // older than the copy: only the missing activity makes it stale
		}, func(t *testing.T, s *sqlite.Store) {
			title := "Retitled by the teammate"
			require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{Title: &title}))
			setUpdatedAt(t, s, "REC-1", later.Add(time.Hour))
		}, false, func(t *testing.T, n *model.Node) {
			assert.Equal(t, "Retitled by the teammate", n.Title, "the stale copy's change applies")
			assert.Equal(t, model.StatusDone, n.Status, "the status the kept closed time belongs to stays")
			assert.NotNil(t, n.ClosedAt)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			local, teammate := newTestStore(t), newTestStore(t)
			uid := newUID(t)
			createPlainTask(t, local, "REC-1", 1, uid, sameIDTime)
			createPlainTask(t, teammate, "REC-1", 1, uid, sameIDTime)
			if tt.teammate != nil {
				tt.teammate(t, teammate)
			}
			file := exportOf(t, teammate)
			if tt.v1 {
				file = asSchemaV1(t, file)
			}
			tt.local(t, local)

			mergeFile(t, local, file)
			node, err := local.GetNode(ctx, "REC-1")
			require.NoError(t, err)
			tt.check(t, node)
			assert.Equal(t, node.ComputeHash(), node.ContentHash, "the content hash matches the merged content")
		})
	}
}

// TestImportReconcile_MergeOfCurrentCopy_AppliesItsClear verifies a field a
// teammate cleared in a copy that has seen the local changes (it holds all
// of the node's activity and is not older) is cleared by the merge.
func TestImportReconcile_MergeOfCurrentCopy_AppliesItsClear(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	uid := newUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, uid, "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, uid, "Shared task")
	cleared := ""
	require.NoError(t, teammate.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Description: &cleared}))
	setUpdatedAt(t, teammate, "REC-1", sameIDTime.Add(48*time.Hour)) // later than the local copy

	mergeFile(t, local, exportOf(t, teammate))
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Empty(t, node.Description, "a current copy's clear applies")
}

// TestImportReconcile_LocalUIDUnderAnotherID_MovesWithoutConfirm verifies a
// local task that the file holds under another id (another clone
// renumbered it) moves there with its subtree, without confirmation, while
// the file's task takes its old id; nothing is lost, the moves are
// reported, and a dependency follows the moved task.
func TestImportReconcile_LocalUIDUnderAnotherID_MovesWithoutConfirm(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	shared, moved, child := newUID(t), newUID(t), newUID(t)
	for _, s := range []*sqlite.Store{local, teammate} {
		createSameIDTask(t, s, "REC-1", "", 1, shared, "Shared task")
	}
	createSameIDTask(t, local, "REC-2", "", 2, moved, "Moved task")
	createSameIDTask(t, local, "REC-2.1", "REC-2", 1, child, "Its subtask")
	require.NoError(t, local.AddDependency(ctx, &model.Dependency{
		FromID: "REC-1", ToID: "REC-2", DepType: model.DepTypeBlocks, CreatedAt: sameIDTime,
	}))
	theirs := createSameIDTask(t, teammate, "REC-2", "", 2, newUID(t), "Teammate task")
	createSameIDTask(t, teammate, "REC-3", "", 3, moved, "Moved task")
	createSameIDTask(t, teammate, "REC-3.1", "REC-3", 1, child, "Its subtask")
	require.NoError(t, teammate.AddDependency(ctx, &model.Dependency{
		FromID: "REC-1", ToID: "REC-3", DepType: model.DepTypeBlocks, CreatedAt: sameIDTime,
	}))
	file := exportOf(t, teammate)

	diff, err := sqlite.DiffReplace(exportOf(t, local), file)
	require.NoError(t, err)
	assert.False(t, diff.Lossy(), "a moved task is not lost: %+v", diff.Losses)

	report := mergeFile(t, local, file)
	assert.Empty(t, report.LocalRenumbers)
	assert.Equal(t, []sqlite.ImportRemapEntry{
		{UID: moved, OldPath: "REC-2", NewPath: "REC-3"},
		{UID: child, OldPath: "REC-2.1", NewPath: "REC-3.1"},
	}, report.Moved)
	assert.Contains(t, report.String(), "uid="+moved+" REC-2 -> REC-3")
	for uid, want := range map[string]string{moved: "REC-3", child: "REC-3.1", theirs: "REC-2"} {
		path, err := local.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, want, path)
	}
	deps, err := local.GetBlockers(ctx, "REC-3")
	require.NoError(t, err)
	assert.Len(t, deps, 1)
}

// TestImportReconcile_UIDsSwappedBetweenIDs_MovesBoth verifies two local
// tasks the file holds under each other's id both move (a swap), with no
// renumbering and no confirmation.
func TestImportReconcile_UIDsSwappedBetweenIDs_MovesBoth(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	a, b := newUID(t), newUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, a, "Task A")
	createSameIDTask(t, local, "REC-2", "", 2, b, "Task B")
	createSameIDTask(t, teammate, "REC-1", "", 1, b, "Task B")
	createSameIDTask(t, teammate, "REC-2", "", 2, a, "Task A")

	report := mergeFile(t, local, exportOf(t, teammate))
	assert.Len(t, report.Moved, 2)
	for uid, want := range map[string]string{a: "REC-2", b: "REC-1"} {
		path, err := local.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, want, path)
	}
}

// TestSameTaskAcrossBackfilledUIDs verifies the same id with the same
// creation time is the same task although the uids differ (each clone
// minted its own uid when it upgraded from before uids were shared): a
// replace loses nothing, a merge needs no renumbering and adopts the
// file's uid. A different creation time, or, when one is missing, a
// different title, is a different task.
func TestSameTaskAcrossBackfilledUIDs(t *testing.T) {
	tests := []struct {
		name          string
		fileCreatedAt string // "" keeps the local creation time
		fileTitle     string
		different     bool
	}{
		{"same creation time", "", "Shared task", false},
		{"same creation time, retitled", "", "Retitled", false},
		{"different creation time", "2026-09-24T09:00:00Z", "Shared task", true},
		{"no creation time, same title", "none", "Shared task", false},
		{"no creation time, other title", "none", "Retitled", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, newUID(t), "Shared task")
			localData := exportOf(t, local)
			file := exportOf(t, local)
			file.Nodes[0].UID, file.Nodes[0].Title = newUID(t), tt.fileTitle
			switch tt.fileCreatedAt {
			case "":
			case "none":
				file.Nodes[0].CreatedAt = ""
			default:
				file.Nodes[0].CreatedAt = tt.fileCreatedAt
			}

			diff, err := sqlite.DiffReplace(localData, file)
			require.NoError(t, err)
			loss := lossOf(diff, "REC-1")
			assert.Equal(t, tt.different, loss != nil && loss.DifferentTask, "%+v", diff.Losses)
		})
	}
}

// TestImportReconcile_BackfilledUIDs_MergeAdoptsFileUID verifies the merge
// of a board whose shared tasks carry other uids with the same creation
// times renumbers nothing and adopts the file's uids, content unchanged.
func TestImportReconcile_BackfilledUIDs_MergeAdoptsFileUID(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, newUID(t), "Shared task")
	fileUID := createSameIDTask(t, teammate, "REC-1", "", 1, newUID(t), "Shared task")

	report := mergeFile(t, local, exportOf(t, teammate))
	assert.Empty(t, report.LocalRenumbers)
	assert.Empty(t, report.Moved)
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, fileUID, node.UID, "the local node adopts the file's uid")
}

// TestImportReconcile_TwoLocalTasksRenumbered_GetDistinctNumbers verifies
// two local tasks renumbered under one parent get different numbers (both
// clones created REC-2 and REC-3).
func TestImportReconcile_TwoLocalTasksRenumbered_GetDistinctNumbers(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	shared := newUID(t)
	for _, s := range []*sqlite.Store{local, teammate} {
		createSameIDTask(t, s, "REC-1", "", 1, shared, "Shared task")
	}
	two := createSameIDTask(t, local, "REC-2", "", 2, newUID(t), "Local two")
	three := createSameIDTask(t, local, "REC-3", "", 3, newUID(t), "Local three")
	createSameIDTask(t, teammate, "REC-2", "", 2, newUID(t), "Their two")
	createSameIDTask(t, teammate, "REC-3", "", 3, newUID(t), "Their three")

	report, _, err := local.ImportReconcile(ctx, exportOf(t, teammate), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []sqlite.ImportRemapEntry{
		{UID: two, OldPath: "REC-2", NewPath: "REC-4"},
		{UID: three, OldPath: "REC-3", NewPath: "REC-5"},
	}, report.LocalRenumbers)
	assert.NotContains(t, report.String(), "not applied", "an applied report has no not-applied line")
}

// TestImportReconcile_RenumberNumbering_PrefixAndSoftDeleted verifies the
// numbering and the id boundaries of a renumber: siblings REC-2 and REC-20
// are separate tasks (REC-20 is not under REC-2), and a soft-deleted
// sibling with the highest number still counts.
func TestImportReconcile_RenumberNumbering_PrefixAndSoftDeleted(t *testing.T) {
	tests := []struct {
		name  string
		local func(t *testing.T, s *sqlite.Store) // besides a different REC-2
		file  []string                            // ids the teammate holds besides REC-2
		want  []string                            // old -> new of the renumbered local tasks
	}{
		{"siblings REC-2 and REC-20", func(t *testing.T, s *sqlite.Store) {
			createSameIDTask(t, s, "REC-20", "", 20, newUID(t), "Local twenty")
		}, []string{"REC-20"}, []string{"REC-2 -> REC-21", "REC-20 -> REC-22"}},
		{"soft-deleted highest sibling", func(t *testing.T, s *sqlite.Store) {
			createSameIDTask(t, s, "REC-5", "", 5, newUID(t), "Deleted five")
			require.NoError(t, s.DeleteNode(context.Background(), "REC-5", false, "agent-local"))
		}, nil, []string{"REC-2 -> REC-6"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, teammate := newTestStore(t), newTestStore(t)
			createSameIDTask(t, local, "REC-2", "", 2, newUID(t), "Local two")
			tt.local(t, local)
			createSameIDTask(t, teammate, "REC-2", "", 2, newUID(t), "Their two")
			for _, id := range tt.file {
				createSameIDTask(t, teammate, id, "", 20, newUID(t), "Their "+id)
			}

			report, _, err := local.ImportReconcile(context.Background(), exportOf(t, teammate),
				sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: true})
			require.NoError(t, err)
			var got []string
			for _, m := range report.LocalRenumbers {
				got = append(got, m.OldPath+" -> "+m.NewPath)
			}
			assert.Equal(t, tt.want, got)
		})
	}
}

// TestImport_MergeOverDifferentTaskSameContentHash_Refused verifies merge
// import refuses a different task under a local id even when both share a
// content hash, the case merge would otherwise skip as unchanged.
func TestImport_MergeOverDifferentTaskSameContentHash_Refused(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, newUID(t), "Same title")
	file := exportOf(t, local)
	file.Nodes[0].UID, file.Nodes[0].CreatedAt = newUID(t), "2026-09-24T09:00:00Z"
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	_, err := local.Import(ctx, file, sqlite.ImportModeMerge, false)
	require.ErrorIs(t, err, model.ErrConflict)
}

// TestImportReconcile_BeforeWrite_RunsOnlyBeforeAWrite verifies the step a
// caller runs before the import writes (mtix import --mode merge backs up
// there) runs once when the import writes, never when it writes nothing
// (confirmation awaited, a file that fails its checks), and that its error
// stops the import.
func TestImportReconcile_BeforeWrite_RunsOnlyBeforeAWrite(t *testing.T) {
	tests := []struct {
		name      string
		edit      func(d *sqlite.ExportData)
		hookErr   error
		wantCalls int
		wantErr   error
	}{
		{"the import writes", nil, nil, 1, nil},
		{"confirmation awaited", func(d *sqlite.ExportData) {
			d.Nodes[0].UID, d.Nodes[0].CreatedAt = "01a0d56f-0000-7000-8000-00000000f001", "2026-09-23T08:00:00Z"
			require.NoError(t, sqlite.RecomputeExportChecksum(d))
		}, nil, 0, sqlite.ErrImportConfirmationRequired},
		{"the file fails its checks", func(d *sqlite.ExportData) { d.Checksum = "stale" }, nil, 0, model.ErrInvalidInput},
		{"the step fails", nil, model.ErrConflict, 1, model.ErrConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, newUID(t), "Local title")
			file := exportOf(t, local)
			file.Nodes[0].Title = "Retitled"
			require.NoError(t, sqlite.RecomputeExportChecksum(file))
			if tt.edit != nil {
				tt.edit(file)
			}
			calls := 0
			_, _, err := local.ImportReconcile(context.Background(), file, sqlite.ImportReconcileOptions{
				Mode: sqlite.ImportModeMerge, BeforeWrite: func() error { calls++; return tt.hookErr },
			})
			assert.Equal(t, tt.wantCalls, calls)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				node, getErr := local.GetNode(context.Background(), "REC-1")
				require.NoError(t, getErr)
				assert.Equal(t, "Local title", node.Title, "nothing was written")
				return
			}
			require.NoError(t, err)
		})
	}
}

// TestImportReconcile_RenumberSkipsANumberWithNodesUnderIt verifies a
// renumbered local task never takes a number that a local or a file node
// sits under although no node holds the number itself (its parent is
// gone): the plan skips it.
func TestImportReconcile_RenumberSkipsANumberWithNodesUnderIt(t *testing.T) {
	tests := []struct {
		name  string
		local func(t *testing.T, s *sqlite.Store)
		file  []sqlite.TestExportNode
	}{
		{"a local node under the number", func(t *testing.T, s *sqlite.Store) {
			createSameIDTask(t, s, "REC-5", "", 5, newUID(t), "Parent gone")
			createSameIDTask(t, s, "REC-5.1", "REC-5", 1, newUID(t), "Left behind")
			_, err := s.WriteDB().ExecContext(context.Background(), `DELETE FROM nodes WHERE id = 'REC-5'`)
			require.NoError(t, err)
		}, nil},
		{"a file node under the number", nil, []sqlite.TestExportNode{{
			ID: "REC-5.1", ParentID: "REC-5", Project: "REC", Depth: 1, Seq: 1, Title: "Orphan",
			ContentHash: "h-o", UID: "01a0d56f-0000-7000-8000-00000000f101", CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
		}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newTestStore(t)
			createSameIDTask(t, local, "REC-2", "", 2, newUID(t), "Local two")
			createSameIDTask(t, local, "REC-4", "", 4, newUID(t), "Local four")
			if tt.local != nil {
				tt.local(t, local)
			}
			nodes := append([]sqlite.TestExportNode{{ID: "REC-2", Project: "REC", Seq: 2, Title: "Their two",
				ContentHash: "h-t", UID: newUID(t), CreatedAt: sameIDTime, UpdatedAt: sameIDTime}}, tt.file...)
			report, _, err := local.ImportReconcile(context.Background(), reconcileExport(t, "REC", nodes...),
				sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
			require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
			require.Len(t, report.LocalRenumbers, 1)
			assert.Equal(t, "REC-6", report.LocalRenumbers[0].NewPath, "REC-5 has a node under it")
		})
	}
}

// TestImportReconcile_MoveOntoAKeptTask_RejectedWritesNothing verifies a
// task the file moves onto an id a local task the merge keeps also holds
// (the same creation time under another uid) is rejected, writing nothing.
func TestImportReconcile_MoveOntoAKeptTask_RejectedWritesNothing(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	kept, moved := newUID(t), newUID(t)
	createPlainTask(t, local, "REC-1", 1, kept, sameIDTime)
	createPlainTask(t, local, "REC-2", 2, moved, sameIDTime)
	before := storeSnapshotJSON(t, local)
	file := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-1", Project: "REC", Seq: 1, Title: "Shared task", ContentHash: "h",
		UID: moved, CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
	})

	_, _, err := local.ImportReconcile(ctx, file, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: true})
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Equal(t, before, storeSnapshotJSON(t, local))
}

// TestImportReconcile_NodeWhoseParentIsGone_KeepsItsID verifies a local
// node whose parent was removed keeps its id through a merge that moves
// nothing, and is not reported as moved.
func TestImportReconcile_NodeWhoseParentIsGone_KeepsItsID(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	shared := newUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, shared, "Shared task")
	createSameIDTask(t, local, "REC-5", "", 5, newUID(t), "Parent gone")
	left := createSameIDTask(t, local, "REC-5.1", "REC-5", 1, newUID(t), "Left behind")
	_, err := local.WriteDB().ExecContext(ctx, `DELETE FROM nodes WHERE id = 'REC-5'`)
	require.NoError(t, err)
	file := reconcileExport(t, "REC", sqlite.TestExportNode{ID: "REC-1", Project: "REC", Seq: 1,
		Title: "Shared task", ContentHash: "h", UID: shared, CreatedAt: createdAtFor("Shared task"),
		UpdatedAt: createdAtFor("Shared task")})

	report := mergeFile(t, local, file)
	assert.Empty(t, report.Moved)
	assert.Empty(t, report.LocalRenumbers)
	path, err := local.ResolveDisplayPathByUID(ctx, left)
	require.NoError(t, err)
	assert.Equal(t, "REC-5.1", path)
}
