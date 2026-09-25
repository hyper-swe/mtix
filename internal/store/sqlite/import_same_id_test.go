// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.4 (FR-15.2i, ADR-003 §6): when the store and an
// imported file hold DIFFERENT tasks under one id (both uids set, not
// equal), a replace is reported as losing "a different task under this
// id", and a merge never overwrites the local task: it renumbers the local
// task and its subtree to the next number free in both the store and the
// file, keeping the uid, reports the remap and applies it only with
// confirmation. A replace checked against the store writes nothing once the
// store has changed. Written red-first against the MTIX-95.31.2 round-3
// code.
package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// sameIDTime is the creation time of every task in these tests.
var sameIDTime = time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)

// createSameIDTask creates task id, number seq, with the given uid and
// title in s at sameIDTime, as a root when parent is empty and as parent's
// child otherwise, and returns the uid. Tasks two stores create this way
// share a creation second; their uids tell them apart (MTIX-95.31.4).
func createSameIDTask(t *testing.T, s *sqlite.Store, id, parent string, seq int, uid, title string) string {
	t.Helper()
	depth := 0
	if parent != "" {
		depth = 1
	}
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: id, ParentID: parent, Project: "REC", Depth: depth, Seq: seq, Title: title,
		Description: "Description of " + title, Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeForDepth(depth),
		ContentHash: "h-" + title, UID: uid, CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
	}))
	return uid
}

// uidMintedAt returns a UUIDv7 whose embedded time is at: the uid CreateNode
// mints for a task created at that time.
func uidMintedAt(t *testing.T, at time.Time) string {
	t.Helper()
	u, err := uuid.NewV7()
	require.NoError(t, err)
	ms := uint64(at.UnixMilli()) //nolint:gosec // a positive test time
	for i := 0; i < 6; i++ {
		u[i] = byte(ms >> (40 - 8*i))
	}
	return u.String()
}

// taskUID returns the uid minted when a task is created at sameIDTime.
func taskUID(t *testing.T) string {
	t.Helper()
	return uidMintedAt(t, sameIDTime)
}

// backfilledUID returns a uid minted long after sameIDTime, as BackfillUIDs
// mints one for a task when a clone upgrades from before uids were shared.
func backfilledUID(t *testing.T) string {
	t.Helper()
	return uidMintedAt(t, sameIDTime.Add(30*24*time.Hour))
}

// storeSnapshotJSON returns the store's export without its export time.
func storeSnapshotJSON(t *testing.T, s *sqlite.Store) string {
	t.Helper()
	data, err := s.Export(context.Background(), "", "")
	require.NoError(t, err)
	data.ExportedAt = ""
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return string(raw)
}

// sameIDStores returns a local store and a teammate's export. Both hold
// REC-1 (one shared task). The local store also holds REC-2 ("Local task",
// with an annotation, a child REC-2.1 and the dependency REC-1 blocks
// REC-2.1); the teammate independently created a different REC-2, with
// its own subtask REC-2.1, and a REC-5 (REC-3 and REC-4 are free in both).
// It returns the local store, the file, and the local uids of REC-2 and
// REC-2.1.
func sameIDStores(t *testing.T) (*sqlite.Store, func() *sqlite.ExportData, string, string) {
	t.Helper()
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	shared := taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, shared, "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, shared, "Shared task")
	mine := createSameIDTask(t, local, "REC-2", "", 2, taskUID(t), "Local task")
	child := createSameIDTask(t, local, "REC-2.1", "REC-2", 1, taskUID(t), "Local subtask")
	require.NoError(t, local.SetAnnotations(ctx, "REC-2", []model.Annotation{{
		ID: "01J9SAMEID0000000000000001", Author: "reviewer", Text: "PASS", CreatedAt: sameIDTime,
	}}))
	require.NoError(t, local.AddDependency(ctx, &model.Dependency{
		FromID: "REC-1", ToID: "REC-2.1", DepType: model.DepTypeBlocks, CreatedAt: sameIDTime,
	}))
	createSameIDTask(t, teammate, "REC-2", "", 2, taskUID(t), "Teammate task")
	createSameIDTask(t, teammate, "REC-2.1", "REC-2", 1, taskUID(t), "Teammate subtask")
	createSameIDTask(t, teammate, "REC-5", "", 5, taskUID(t), "Teammate later task")
	file := func() *sqlite.ExportData {
		data, err := teammate.Export(ctx, "", "")
		require.NoError(t, err)
		return data
	}
	return local, file, mine, child
}

// TestImportReconcile_SameIDDifferentUID_RenumbersLocalSubtreeOnlyWithConfirm
// verifies the local REC-2 and its subtree move to REC-6, the number after
// the highest the store or the file holds (the file holds REC-5), keeping
// uids, annotations and dependencies, and the file's REC-2 takes the id;
// without confirmation nothing is written.
func TestImportReconcile_SameIDDifferentUID_RenumbersLocalSubtreeOnlyWithConfirm(t *testing.T) {
	ctx := context.Background()
	local, file, mine, child := sameIDStores(t)
	require.Equal(t, "REC-2", file().Nodes[1].ID)
	teammateREC2 := file().Nodes[1].UID
	before := storeSnapshotJSON(t, local)
	wantRemap := []sqlite.ImportRemapEntry{
		{UID: mine, OldPath: "REC-2", NewPath: "REC-6"},
		{UID: child, OldPath: "REC-2.1", NewPath: "REC-6.1"},
	}

	report, result, err := local.ImportReconcile(ctx, file(), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
	assert.Nil(t, result)
	require.NotNil(t, report)
	assert.False(t, report.Applied)
	assert.Equal(t, wantRemap, report.LocalRenumbers)
	assert.Contains(t, report.String(), "uid="+mine+" REC-2 -> REC-6")
	assert.Contains(t, report.String(), "--confirm")
	assert.Equal(t, before, storeSnapshotJSON(t, local), "without confirmation nothing is written")

	report, _, err = local.ImportReconcile(ctx, file(), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assert.True(t, report.Applied)
	assert.Equal(t, wantRemap, report.LocalRenumbers)

	theirs, err := local.GetNode(ctx, "REC-2")
	require.NoError(t, err)
	assert.Equal(t, teammateREC2, theirs.UID, "the file's task keeps its id")
	assert.Equal(t, "Teammate task", theirs.Title)
	moved, err := local.GetNode(ctx, "REC-6")
	require.NoError(t, err)
	assert.Equal(t, mine, moved.UID)
	assert.Equal(t, "Local task", moved.Title)
	assert.Equal(t, "Description of Local task", moved.Description)
	require.Len(t, moved.Annotations, 1)
	assert.Equal(t, "01J9SAMEID0000000000000001", moved.Annotations[0].ID)
	sub, err := local.GetNode(ctx, "REC-6.1")
	require.NoError(t, err)
	assert.Equal(t, child, sub.UID)
	assert.Equal(t, "REC-6", sub.ParentID)
	theirSub, err := local.GetNode(ctx, "REC-2.1")
	require.NoError(t, err)
	assert.Equal(t, "Teammate subtask", theirSub.Title, "the file's subtask under the file's task")
	deps, err := local.GetBlockers(ctx, "REC-6.1")
	require.NoError(t, err)
	require.Len(t, deps, 1, "the dependency follows the moved subtask")
	assert.Equal(t, "REC-1", deps[0].FromID)

	next, err := local.NextSequence(ctx, "REC:")
	require.NoError(t, err)
	assert.Equal(t, 7, next, "a later create does not reuse the renumbered task's number")
}

// TestImportReconcile_SameIDDifferentUID_ChildAndSoftDeleted verifies the
// rule at depth and for soft-deleted tasks: a local child that is a
// different task than the file's child under a shared parent moves to the
// next child number, and a soft-deleted local task is moved too, still
// deleted, instead of being overwritten, to the number after the highest
// the store holds (a local REC-4, which the file lacks).
func TestImportReconcile_SameIDDifferentUID_ChildAndSoftDeleted(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	shared := taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, shared, "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, shared, "Shared task")
	localChild := createSameIDTask(t, local, "REC-1.1", "REC-1", 1, taskUID(t), "Local child")
	deleted := createSameIDTask(t, local, "REC-2", "", 2, taskUID(t), "Deleted locally")
	require.NoError(t, local.DeleteNode(ctx, "REC-2", false, "agent-local"))
	createSameIDTask(t, local, "REC-4", "", 4, taskUID(t), "Local later task")
	createSameIDTask(t, teammate, "REC-1.1", "REC-1", 1, taskUID(t), "Teammate child")
	createSameIDTask(t, teammate, "REC-2", "", 2, taskUID(t), "Teammate REC-2")
	data, err := teammate.Export(ctx, "", "")
	require.NoError(t, err)

	report, _, err := local.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []sqlite.ImportRemapEntry{
		{UID: deleted, OldPath: "REC-2", NewPath: "REC-5"},
		{UID: localChild, OldPath: "REC-1.1", NewPath: "REC-1.2"},
	}, report.LocalRenumbers, "shallowest first")
	path, err := local.ResolveDisplayPathByUID(ctx, localChild)
	require.NoError(t, err)
	assert.Equal(t, "REC-1.2", path)
	after, err := local.Export(ctx, "", "")
	require.NoError(t, err)
	moved := exportedNode(t, after, "REC-5")
	assert.JSONEq(t, `"`+deleted+`"`, string(moved["uid"]), "the soft-deleted task moved to REC-5")
	assert.NotEmpty(t, moved["deleted_at"], "and is still soft-deleted")
	theirs, err := local.GetNode(ctx, "REC-2")
	require.NoError(t, err)
	assert.Equal(t, "Teammate REC-2", theirs.Title)
}

// TestImportReconcile_SameIDSameOrMissingUID_NoRenumber verifies the same
// uid, or a file without uids (an older export) whose task has the local
// title, updates the node in place (MTIX-95.31.9: without a uid to compare,
// another title is another task, see import_uidless_title_test.go).
func TestImportReconcile_SameIDSameOrMissingUID_NoRenumber(t *testing.T) {
	tests := []struct {
		name    string
		fileUID func(local string) string
		title   string
	}{
		{"same uid", func(local string) string { return local }, "Retitled"},
		{"file without uid, the local title", func(string) string { return "" }, "Local title"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			local := newTestStore(t)
			uid := createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local title")
			data := reconcileExport(t, "REC", sqlite.TestExportNode{
				ID: "REC-1", Project: "REC", Title: tt.title, Seq: 1, ContentHash: "h-new",
				UID: tt.fileUID(uid), CreatedAt: sameIDTime, UpdatedAt: sameIDTime.Add(time.Hour),
			})

			report, _, err := local.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
			require.NoError(t, err)
			assert.Empty(t, report.LocalRenumbers)
			node, err := local.GetNode(ctx, "REC-1")
			require.NoError(t, err)
			assert.Equal(t, tt.title, node.Title)
			assert.Equal(t, uid, node.UID)
		})
	}
}

// TestImport_MergeOverDifferentTask_RefusedWritesNothing verifies merge
// import itself never overwrites a local task with a different task that
// holds its id, even when called without the reconciliation that renumbers
// it first.
func TestImport_MergeOverDifferentTask_RefusedWritesNothing(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local title")
	before := storeSnapshotJSON(t, local)
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-1", Project: "REC", Title: "Another task", Seq: 1, ContentHash: "h-other",
		UID: taskUID(t), CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
	})

	_, err := local.Import(ctx, data, sqlite.ImportModeMerge, false)
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Contains(t, err.Error(), "a different task under this id")
	assert.Equal(t, before, storeSnapshotJSON(t, local))
}

// TestDiffReplace_SameIDDifferentUID_ReportsDifferentTask verifies a node
// whose id holds different non-empty uids locally and in the file is lost
// as a whole task, named with both titles, while the same uid, or a missing
// uid with the local title, is compared field by field as before (a missing
// uid with another title: TestDiffReplace_SameTaskAtUpgradeWithOtherTitle_ListedAsLoss).
func TestDiffReplace_SameIDDifferentUID_ReportsDifferentTask(t *testing.T) {
	tests := []struct {
		name      string
		fileUID   func(local string) string
		fileTitle string
		wantLoss  bool
	}{
		{"different uid", func(string) string { return "01J9OTHERTASK0000000000001" }, "File title", true},
		{"same uid", func(local string) string { return local }, "File title", false},
		{"file without uid, the local title", func(string) string { return "" }, "Local title", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newTestStore(t)
			uid := createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local title")
			localData, err := local.Export(context.Background(), "", "")
			require.NoError(t, err)
			var file sqlite.ExportData
			raw, err := json.Marshal(localData)
			require.NoError(t, err)
			require.NoError(t, json.Unmarshal(raw, &file))
			file.Nodes[0].UID = tt.fileUID(uid)
			file.Nodes[0].Title = tt.fileTitle
			file.Nodes[0].CreatedAt = "2026-09-23T08:00:00Z" // created elsewhere, at another time

			diff, err := sqlite.DiffReplace(localData, &file)
			require.NoError(t, err)
			loss := lossOf(diff, "REC-1")
			if !tt.wantLoss {
				assert.False(t, diff.Lossy(), "%+v", diff.Losses)
				return
			}
			require.NotNil(t, loss)
			assert.True(t, loss.DifferentTask)
			assert.Equal(t, "Local title", loss.LocalTitle)
			assert.Equal(t, "File title", loss.FileTitle)
			assert.True(t, diff.Lossy())
			assert.Equal(t, []string{"REC-1"}, diff.Updated)
		})
	}
}

// TestImport_IfStoreUnchanged_WritesNothingAfterAChange verifies a replace
// checked against the store's export checksum writes nothing once the
// store has changed, so the write survives, and applies when the store is
// unchanged.
func TestImport_IfStoreUnchanged_WritesNothingAfterAChange(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local title")
	checked, err := local.Export(ctx, "", "")
	require.NoError(t, err)
	file := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-7", Project: "REC", Title: "From the file", Seq: 7, ContentHash: "h7",
		UID: taskUID(t), CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
	})

	title := "Written after the check"
	require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Title: &title}))
	_, err = local.Import(ctx, file, sqlite.ImportModeReplace, false, sqlite.IfStoreUnchanged(checked.Checksum))
	require.ErrorIs(t, err, sqlite.ErrStoreChangedSinceCheck)
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err, "the write survives")
	assert.Equal(t, title, node.Title)
	_, err = local.GetNode(ctx, "REC-7")
	assert.ErrorIs(t, err, model.ErrNotFound, "nothing was imported")

	current, err := local.Export(ctx, "", "")
	require.NoError(t, err)
	_, err = local.Import(ctx, file, sqlite.ImportModeReplace, false, sqlite.IfStoreUnchanged(current.Checksum))
	require.NoError(t, err)
	_, err = local.GetNode(ctx, "REC-7")
	assert.NoError(t, err, "an unchanged store is replaced")
}

// TestImportReconcile_LocalRenumberAndProvisional_TakeDistinctNumbers
// verifies a local task renumbered by a merge and an incoming provisional
// node under the same parent get different numbers: the provisional
// renumbering avoids the number the local task takes.
func TestImportReconcile_LocalRenumberAndProvisional_TakeDistinctNumbers(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	shared, provUID := taskUID(t), taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, shared, "Shared task")
	mine := createSameIDTask(t, local, "REC-1.1", "REC-1", 1, taskUID(t), "Local child")
	data := reconcileExport(t, "REC",
		sqlite.TestExportNode{ID: "REC-1", Project: "REC", Seq: 1, Title: "Shared task",
			ContentHash: "h-Shared task", UID: shared, CreatedAt: sameIDTime, UpdatedAt: sameIDTime},
		sqlite.TestExportNode{ID: "REC-1.1", ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1,
			Title: "Teammate child", ContentHash: "h-tc", UID: taskUID(t), CreatedAt: sameIDTime, UpdatedAt: sameIDTime},
		sqlite.TestExportNode{ID: provisionalPath(t, "REC-1", provUID), ParentID: "REC-1", Project: "REC",
			Depth: 1, Seq: 1, Title: "Provisional child", ContentHash: "h-pc", UID: provUID,
			CreatedAt: sameIDTime, UpdatedAt: sameIDTime},
	)

	report, _, err := local.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Confirm: true,
	})
	require.NoError(t, err)
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: mine, OldPath: "REC-1.1", NewPath: "REC-1.2"}}, report.LocalRenumbers)
	require.Len(t, report.Remaps, 1)
	assert.Equal(t, "REC-1.3", report.Remaps[0].NewPath)
	for uid, want := range map[string]string{mine: "REC-1.2", provUID: "REC-1.3"} {
		path, err := local.ResolveDisplayPathByUID(ctx, uid)
		require.NoError(t, err)
		assert.Equal(t, want, path)
	}
}

// TestImportReconcile_ReplaceMode_NoLocalRenumber verifies only a merge
// renumbers a local task: a replace import, which keeps no local task,
// plans no renumbering and needs no confirmation.
func TestImportReconcile_ReplaceMode_NoLocalRenumber(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local title")
	theirs := taskUID(t)
	data := reconcileExport(t, "REC", sqlite.TestExportNode{
		ID: "REC-1", Project: "REC", Title: "File title", Seq: 1, ContentHash: "h-file",
		UID: theirs, CreatedAt: sameIDTime, UpdatedAt: sameIDTime,
	})

	report, _, err := local.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeReplace})
	require.NoError(t, err)
	assert.Empty(t, report.LocalRenumbers)
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, theirs, node.UID, "the file wins a replace")
}
