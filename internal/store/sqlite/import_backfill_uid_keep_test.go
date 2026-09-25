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
	"testing"

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
		want     []string // the fields lost by REC-1
	}{
		{"a marked backfill uid", importBackfillUID, nil},
		{"a create's UUIDv7", taskUID, []string{"uid"}},
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
	node, err := local.GetNode(ctx, "REC-1")
	require.NoError(t, err)
	assert.Equal(t, "Retitled by the teammate", node.Title)
	assert.Equal(t, theirs, node.UID)
	assert.Len(t, exportOf(t, local).Nodes, 1, "no duplicate")
}
