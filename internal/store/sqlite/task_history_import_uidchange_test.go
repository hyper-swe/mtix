// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func historyCount(t *testing.T, db *sql.DB, where string, args ...any) int {
	t.Helper()
	var n int
	// where is a fixed literal from the calling test.
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history `+where, args...).Scan(&n))
	return n
}

// legacyHistory makes REC-1 a legacy task: no uid, history keyed by its id.
func legacyHistory(t *testing.T, db *sql.DB) {
	t.Helper()
	_, err := db.Exec(`UPDATE task_history SET task_uid = 'REC-1'`)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE nodes SET uid = NULL WHERE id = 'REC-1'`)
	require.NoError(t, err)
}

// TestTaskHistory_ImportReplace_UIDlessTaskRenamedInTheFile_IsADifferentTask
// pins the replace-mode identity rule for tasks without a uid: the same id
// with another title is another task (the import's own rule, MTIX-95.31.9),
// so the file's task starts its history and the earlier entries stay under
// the old key.
func TestTaskHistory_ImportReplace_UIDlessTaskRenamedInTheFile_IsADifferentTask(t *testing.T) {
	ctx := context.Background()
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Shared task")
	who := "carol"
	require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Assignee: &who}))
	file := asSchemaV1(t, exportOf(t, local))
	file.Nodes[0].UID, file.Nodes[0].Title = "", "Shared task renamed"
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	db := local.WriteDB()
	legacyHistory(t, db)

	_, err := local.Import(ctx, file, sqlite.ImportModeReplace, true)
	require.NoError(t, err)

	assert.Positive(t, historyCount(t, db, `WHERE origin = 'import' AND field = 'title' AND old_value IS NULL`), "the file's task has its own creation entries")
	assert.Zero(t, historyCount(t, db, `WHERE origin = 'import' AND old_value LIKE '%carol%'`), "no value of the earlier task leaks")
}

// TestTaskHistory_ImportReplace_UIDlessFileOverUIDlessTask_PairsNothing: the
// replace import does not pair a uid-less local task with the file's uid-less
// task (it mints the file's task a new uid), so the recorder does not either:
// the old history stays under the old key and the task starts fresh. Leaving
// history behind is acceptable; grafting it onto the wrong task is not.
func TestTaskHistory_ImportReplace_UIDlessFileOverUIDlessTask_PairsNothing(t *testing.T) {
	ctx := context.Background()
	prio := model.PriorityHigh
	local := newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, taskUID(t), "Shared task")
	require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Priority: &prio}))
	file := asSchemaV1(t, exportOf(t, local))
	file.Nodes[0].UID = ""
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	db := local.WriteDB()
	legacyHistory(t, db)
	old := historyCount(t, db, `WHERE task_uid = 'REC-1'`)

	_, err := local.Import(ctx, file, sqlite.ImportModeReplace, true)
	require.NoError(t, err)

	var uid string
	require.NoError(t, db.QueryRow(`SELECT COALESCE(uid, '') FROM nodes WHERE id = 'REC-1'`).Scan(&uid))
	assert.Equal(t, old, historyCount(t, db, `WHERE task_uid = 'REC-1'`), "the old history stays where it was")
	assert.Positive(t, historyCount(t, db, `WHERE task_uid = ? AND origin = 'import'`, uid), "the new task starts fresh")
	assert.Zero(t, historyCount(t, db, `WHERE task_uid = ? AND origin <> 'import'`, uid), "none of the old history is on it")
}
