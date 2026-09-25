// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.9 (FR-7.8): when either task under one id has no
// uid to compare (a board written before uids were shared, or a local task
// imported from one), a merge takes two different titles for two different
// tasks. It never overwrites the local task: it renumbers it, with its
// subtree, only with confirmation, gives it a uid when it has none, and its
// report names the pair. One title under the id is still one task. Written
// red-first against the MTIX-95.31.6 code, which overwrote the local task.
package sqlite_test

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// clearUIDs removes every uid from s, as a store or board from before uids
// were shared holds its tasks.
func clearUIDs(t *testing.T, s *sqlite.Store) {
	t.Helper()
	_, err := s.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = NULL`)
	require.NoError(t, err)
}

// uidAtID returns the uid node id holds in s ("" for none).
func uidAtID(t *testing.T, s *sqlite.Store, id string) string {
	t.Helper()
	var uid string
	// The node's uid, soft-deleted rows included.
	require.NoError(t, s.WriteDB().QueryRowContext(context.Background(),
		`SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, id).Scan(&uid))
	return uid
}

// TestImportReconcile_NoUIDToCompareAndAnotherTitle_RenumberedOnlyWithConfirm
// verifies a merge of a file whose task under a local id has another title,
// where either side has no uid, writes nothing without confirmation and
// reports the renumbering and the pair; with confirmation the file's task
// takes the id and the local task, with its subtree and a uid, the next
// number free in both.
func TestImportReconcile_NoUIDToCompareAndAnotherTitle_RenumberedOnlyWithConfirm(t *testing.T) {
	tests := []struct {
		name                 string
		localUID, teammateID bool // whether the local and the teammate's task keep a uid
	}{
		{"the file's task has no uid", true, false},
		{"the local task has no uid", false, true},
		{"neither has a uid", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			local, teammate := newTestStore(t), newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local task")
			createSameIDTask(t, local, "REC-1.1", "REC-1", 1, taskUID(t), "Its subtask")
			createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Teammate task")
			if !tt.localUID {
				clearUIDs(t, local)
			}
			if !tt.teammateID {
				clearUIDs(t, teammate)
			}
			file := exportOf(t, teammate)
			before := storeSnapshotJSON(t, local)

			report, _, err := local.ImportReconcile(ctx, file, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
			require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
			assert.Equal(t, before, storeSnapshotJSON(t, local), "without --confirm nothing is written")
			require.Len(t, report.LocalRenumbers, 2)
			assert.Equal(t, "REC-1 -> REC-2", report.LocalRenumbers[0].OldPath+" -> "+report.LocalRenumbers[0].NewPath)
			assert.Equal(t, "REC-1.1 -> REC-2.1", report.LocalRenumbers[1].OldPath+" -> "+report.LocalRenumbers[1].NewPath)
			assert.Equal(t, []sqlite.ImportTitleMismatch{{ID: "REC-1", LocalTitle: "Local task", FileTitle: "Teammate task"}},
				report.TitleMismatches)
			assert.Contains(t, report.String(), "  of these, a task under this id with a different title and no uid "+
				"to compare: 1\n    - REC-1 (local \"Local task\", file \"Teammate task\")\n")
			assert.Equal(t, !tt.localUID, report.LocalRenumbers[0].NewUID, "a uid minted by this import is new")
			assert.Equal(t, !tt.localUID, strings.Contains(report.String(), "    - uid=(new) REC-1 -> REC-2\n"))
			assert.Empty(t, report.UIDAdoptions)

			report, _, err = local.ImportReconcile(ctx, exportOf(t, teammate), sqlite.ImportReconcileOptions{
				Mode: sqlite.ImportModeMerge, Confirm: true,
			})
			require.NoError(t, err)
			for id, title := range map[string]string{"REC-1": "Teammate task", "REC-2": "Local task", "REC-2.1": "Its subtask"} {
				node, getErr := local.GetNode(ctx, id)
				require.NoError(t, getErr)
				assert.Equal(t, title, node.Title, "node %s", id)
			}
			moved := uidAtID(t, local, "REC-2")
			require.NotEmpty(t, moved, "a renumbered task without a uid is given one")
			assert.Equal(t, report.LocalRenumbers[0].UID, moved, "the report names the uid it moved")
		})
	}
}

// TestImportReconcile_NoUIDToCompareOneTitle_MergedAsOneTask verifies a
// file task without a uid under a local id with the same title is still
// merged into the local task (no renumbering), and the local uid stays.
func TestImportReconcile_NoUIDToCompareOneTitle_MergedAsOneTask(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	mine := createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, backfilledUID(t), "Shared task")
	clearUIDs(t, teammate)
	file := exportOf(t, teammate)
	file.Nodes[0].Description, file.Nodes[0].ContentHash = "Teammate's description", "h-teammate"
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	report := mergeFile(t, local, file)
	assert.Empty(t, report.LocalRenumbers)
	assert.Empty(t, report.TitleMismatches)
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, "Teammate's description", node.Description, "the file's change applies")
	assert.Equal(t, mine, node.UID)
}

// TestImport_MergeOverNoUIDTaskWithAnotherTitle_Refused verifies a merge
// that did not renumber first (Store.Import) refuses a file task without a
// uid, under a local id, with another title, and writes nothing.
func TestImport_MergeOverNoUIDTaskWithAnotherTitle_Refused(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Local task")
	before := storeSnapshotJSON(t, local)
	file := exportOf(t, local)
	file.Nodes[0].UID, file.Nodes[0].Title, file.Nodes[0].ContentHash = "", "Teammate task", "h-teammate"
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	_, err := local.Import(ctx, file, sqlite.ImportModeMerge, false)
	require.ErrorIs(t, err, model.ErrConflict)
	assert.Equal(t, before, storeSnapshotJSON(t, local))
}

// TestImportReconcile_NoUIDToCompareUnderMovedParent_ReportsTheFinalID
// verifies a local child whose parent another clone renumbered, and whose
// id under the moved parent the file gives to a task without a uid and with
// another title, is renumbered and reported under the id it collides at
// (REC-3.1), not its old one (MTIX-95.31.9).
func TestImportReconcile_NoUIDToCompareUnderMovedParent_ReportsTheFinalID(t *testing.T) {
	local, teammate := newTestStore(t), newTestStore(t)
	parent, child := taskUID(t), taskUID(t)
	createSameIDTask(t, local, "REC-2", "", 2, parent, "Moved parent")
	createSameIDTask(t, local, "REC-2.1", "REC-2", 1, child, "Local child")
	createSameIDTask(t, teammate, "REC-3", "", 3, parent, "Moved parent")
	createSameIDTask(t, teammate, "REC-3.1", "REC-3", 1, taskUID(t), "Teammate child")
	_, err := teammate.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = NULL WHERE id = 'REC-3.1'`)
	require.NoError(t, err)

	report, _, err := local.ImportReconcile(context.Background(), exportOf(t, teammate),
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: parent, OldPath: "REC-2", NewPath: "REC-3"}}, report.Moved)
	assert.Equal(t, []sqlite.ImportRemapEntry{{UID: child, OldPath: "REC-2.1", NewPath: "REC-3.2"}}, report.LocalRenumbers)
	assert.Equal(t, []sqlite.ImportTitleMismatch{{ID: "REC-3.1", LocalTitle: "Local child", FileTitle: "Teammate child"}},
		report.TitleMismatches)
}
