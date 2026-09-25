// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.9, round 3: a board without uids (from a client at
// 0.3 or older) that holds a task whose local uid is a marked backfill uid
// loses nothing by lacking the uid: the automatic import's comparison does
// not count it, and the replace keeps the local uid, so every later pull of
// such a board imports as before. A create-time uid (UUIDv7) is still a
// loss (MTIX-95.31.4). A merge never decides identity on a local task
// without a uid: it gives it a backfill uid first. Written red-first
// against round 2.
package sqlite_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestDiffReplace_UIDlessCopyOfTask_UIDIsLostOnlyForACreateUID verifies a
// board without uids (schema 1.0.0, never a current copy) that holds a
// task under its id with its title loses nothing when the local uid is a
// marked backfill uid, and still loses "uid" when it is a create's UUIDv7.
func TestDiffReplace_UIDlessCopyOfTask_UIDIsLostOnlyForACreateUID(t *testing.T) {
	tests := []struct {
		name     string
		localUID func(t *testing.T) string
		want     []string // the fields lost by REC-1 (another title: a task the replace does not keep the uid for)
	}{
		{"a marked backfill uid", importBackfillUID, nil},
		{"a create's UUIDv7", taskUID, []string{"uid"}},
		{"a marked backfill uid, another title", importBackfillUID, []string{"uid"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local := newTestStore(t)
			createSameIDTask(t, local, "REC-1", "", 1, tt.localUID(t), "Shared task")
			// A task imported from such a board holds no activity.
			_, err := local.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET activity = NULL`)
			require.NoError(t, err)
			file := asSchemaV1(t, exportOf(t, local))
			file.Nodes[0].UID = ""
			if strings.HasSuffix(tt.name, "another title") {
				file.Nodes[0].Title = "Another task"
			}
			require.NoError(t, sqlite.RecomputeExportChecksum(file))

			diff, err := sqlite.DiffReplace(exportOf(t, local), file)
			require.NoError(t, err)
			loss := lossOf(diff, "REC-1")
			if tt.want == nil {
				assert.Nil(t, loss, "losses: %+v", diff.Losses)
				assert.Empty(t, diff.Updated, "only the uid differs, and the replace keeps it")
				return
			}
			require.NotNil(t, loss)
			assert.Equal(t, tt.want, loss.Fields)
		})
	}
}

// TestImport_ReplaceWithUIDlessCopy_KeepsLocalBackfillUID verifies a
// replace import of a board without uids keeps, for a task at the same id,
// the local uid when it is a marked backfill uid, and only then: a create's
// UUIDv7 is replaced by a new backfill uid, and a backfill uid the board
// holds on another task is not given to a second one.
func TestImport_ReplaceWithUIDlessCopy_KeepsLocalBackfillUID(t *testing.T) {
	tests := []struct {
		name      string
		localUID  func(t *testing.T) string
		uidOnREC2 bool // the board holds REC-1's local uid on REC-2
		kept      bool
	}{
		{"a marked backfill uid", importBackfillUID, false, true},
		{"a create's UUIDv7", taskUID, false, false},
		{"a marked backfill uid the board holds on another task", importBackfillUID, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			local := newTestStore(t)
			uid := createSameIDTask(t, local, "REC-1", "", 1, tt.localUID(t), "Shared task")
			file := exportOf(t, local)
			file.Nodes[0].UID = ""
			added := file.Nodes[0]
			added.ID, added.Seq, added.Title, added.UID = "REC-2", 2, "Other task", ""
			if tt.uidOnREC2 {
				added.UID = uid
			}
			file.Nodes = append(file.Nodes, added)
			file.NodeCount = len(file.Nodes)
			require.NoError(t, sqlite.RecomputeExportChecksum(file))

			_, err := local.Import(ctx, file, sqlite.ImportModeReplace, false)
			require.NoError(t, err)
			got := uidAtID(t, local, "REC-1")
			assert.Equal(t, tt.kept, got == uid, "REC-1 keeps its local uid: %v", tt.kept)
			assert.True(t, model.IsBackfillUID(got) || got == uid, "REC-1 carries a uid")
			assert.NotEqual(t, uidAtID(t, local, "REC-2"), got, "no two tasks share a uid")
		})
	}
}

// TestImportReconcile_LocalTaskWithoutUID_ComparedByItsBackfillUID verifies
// a merge gives a local task without a uid (an open whose backfill failed)
// a backfill uid before it decides identity (MTIX-95.31.9): a teammate's
// retitle of that task, on a board with uids and the same creation time,
// is the same task, merged in place with no renumbering and no duplicate.
func TestImportReconcile_LocalTaskWithoutUID_ComparedByItsBackfillUID(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Shared task")
	clearUIDs(t, local)
	theirs := createSameIDTask(t, teammate, "REC-1", "", 1, taskUID(t), "Retitled by the teammate")

	report := mergeFile(t, local, exportOf(t, teammate))
	assert.Empty(t, report.LocalRenumbers)
	assert.Empty(t, report.TitleMismatches)
	assert.Equal(t, []sqlite.ImportUIDAdoption{{ID: "REC-1", FileUID: theirs, LocalTitle: "Shared task",
		FileTitle: "Retitled by the teammate"}}, report.UIDAdoptions)
	assert.Contains(t, report.String(), "    - REC-1 local uid=(new) -> file uid="+theirs)
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, "Retitled by the teammate", node.Title)
	assert.Equal(t, theirs, node.UID)
	assert.Len(t, exportOf(t, local).Nodes, 1, "no duplicate")
}

// TestImport_ReplaceWithAnotherUIDlessTask_MintsNewUIDSoTeammateSeesADifferentTask
// verifies a replace keeps the local backfill uid only for a file task with
// the same title (MTIX-95.31.9, round 4): a task without a uid and with
// another title at the id is a different task, so it gets a new backfill
// uid, and a teammate who holds the replaced task under the old uid sees a
// different task under the id instead of taking it silently.
func TestImport_ReplaceWithAnotherUIDlessTask_MintsNewUIDSoTeammateSeesADifferentTask(t *testing.T) {
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	alpha := importBackfillUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, alpha, "Alpha")
	createSameIDTask(t, teammate, "REC-1", "", 1, alpha, "Alpha")
	file := exportOf(t, local)
	file.Nodes[0].UID, file.Nodes[0].Title = "", "Beta"
	file.Nodes[0].CreatedAt = sameIDTime.Add(time.Hour).Format(time.RFC3339) // created elsewhere, at another time
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	_, err := local.Import(ctx, file, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	beta := uidAtID(t, local, "REC-1")
	assert.NotEqual(t, alpha, beta, "the different task does not inherit the replaced task's uid")
	assert.True(t, model.IsBackfillUID(beta))

	diff, err := sqlite.DiffReplace(exportOf(t, teammate), exportOf(t, local))
	require.NoError(t, err)
	loss := lossOf(diff, "REC-1")
	require.NotNil(t, loss, "the teammate's import is refused: %+v", diff.Losses)
	assert.True(t, loss.DifferentTask)
}

// TestImport_ReplaceWithTheFileUID_KeepsTheFileUID verifies a file task
// that carries its own uid keeps it through a replace, even where the
// local task at the id carries a backfill uid (MTIX-95.31.9, round 4).
func TestImport_ReplaceWithTheFileUID_KeepsTheFileUID(t *testing.T) {
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, importBackfillUID(t), "Shared task")
	file := exportOf(t, local)
	theirs := taskUID(t)
	file.Nodes[0].UID = theirs
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	_, err := local.Import(context.Background(), file, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	assert.Equal(t, theirs, uidAtID(t, local, "REC-1"))
}

// TestDiffReplace_CopyWithItsOwnUID_IsAnUpdate verifies a file copy that
// carries its own uid is compared with that uid, never the local backfill
// uid (MTIX-95.31.9, round 4): the same task under another backfill uid,
// with the same title, loses nothing, and the replace updates it (it
// writes the file's uid).
func TestDiffReplace_CopyWithItsOwnUID_IsAnUpdate(t *testing.T) {
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, importBackfillUID(t), "Shared task")
	file := exportOf(t, local)
	file.Nodes[0].UID = importBackfillUID(t)
	require.NoError(t, sqlite.RecomputeExportChecksum(file))

	diff, err := sqlite.DiffReplace(exportOf(t, local), file)
	require.NoError(t, err)
	assert.False(t, diff.Lossy(), "losses: %+v", diff.Losses)
	assert.Equal(t, []string{"REC-1"}, diff.Updated)
}
