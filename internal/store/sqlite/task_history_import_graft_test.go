// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The history recorder never decides identity itself: history moves only where
// the import's own pairing moved the task (a shared uid, a merge update of the
// same row, a stamp, a renumber, an adoption it carries). Where the import does
// not pair two rows, the old history stays under its old key and the new task
// starts fresh. Leaving history behind is acceptable; grafting it onto another
// task never is.

func ownedBy(t *testing.T, tag string) func() {
	t.Helper()
	t.Setenv("MTIX_AUTHOR_ID", tag)
	return func() {}
}

// keysOfTag returns the history keys that hold entries recorded under actor tag.
func keysOfTag(t *testing.T, db *sql.DB, tag string) map[string]int {
	t.Helper()
	rows, err := db.Query(`SELECT task_uid, COUNT(*) FROM task_history WHERE actor = ? GROUP BY task_uid`, tag)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	out := map[string]int{}
	for rows.Next() {
		var k string
		var n int
		require.NoError(t, rows.Scan(&k, &n))
		out[k] = n
	}
	require.NoError(t, rows.Err())
	return out
}

// graftSetup builds a local store with A at REC-1 and B at REC-2 (both titled
// the same), each with its own tagged history, and a teammate file holding B at
// REC-1 and a new C at REC-2.
func graftSetup(t *testing.T, uidA, uidB, uidC string) (*sqlite.Store, *sqlite.ExportData) {
	t.Helper()
	ctx := context.Background()
	local, mate := newTestStore(t), newTestStore(t)
	createSameIDTask(t, local, "REC-1", "", 1, uidA, "Write tests")
	createSameIDTask(t, local, "REC-2", "", 2, uidB, "Write tests")
	createSameIDTask(t, mate, "REC-1", "", 1, uidB, "Write tests")
	createSameIDTask(t, mate, "REC-2", "", 2, uidC, "Write tests")
	secret := "alice-secret"
	restore := ownedBy(t, "own-A")
	require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Assignee: &secret}))
	restore()
	restore = ownedBy(t, "own-B")
	b := "bob"
	require.NoError(t, local.UpdateNode(ctx, "REC-2", &store.NodeUpdate{Assignee: &b}))
	restore()
	return local, exportOf(t, mate)
}

// TestTaskHistory_ImportReplace_TaskMovedOntoAnotherTasksID_GraftsNothing (X1):
// B moves from REC-2 to REC-1 and a new C takes REC-2. B's history follows B
// (same uid); A's stays under A; nothing of A's reaches B or C.
func TestTaskHistory_ImportReplace_TaskMovedOntoAnotherTasksID_GraftsNothing(t *testing.T) {
	uidA, uidB, uidC := taskUID(t), taskUID(t), taskUID(t)
	local, file := graftSetup(t, uidA, uidB, uidC)
	t.Setenv("MTIX_AUTHOR_ID", "importer")

	_, err := local.Import(context.Background(), file, sqlite.ImportModeReplace, true)
	require.NoError(t, err)

	db := local.WriteDB()
	assert.Equal(t, map[string]int{uidA: keysOfTag(t, db, "own-A")[uidA]}, keysOfTag(t, db, "own-A"), "A's entries stay under A")
	assert.Equal(t, map[string]int{uidB: keysOfTag(t, db, "own-B")[uidB]}, keysOfTag(t, db, "own-B"), "B's entries stay under B")
	assert.Zero(t, historyCount(t, db, `WHERE task_uid IN (?, ?) AND (old_value LIKE '%alice-secret%' OR new_value LIKE '%alice-secret%')`, uidB, uidC), "A's values reach neither B nor C")
	assert.Zero(t, historyCount(t, db, `WHERE task_uid = ? AND origin = 'import' AND field IN ('title', 'status', 'priority', 'weight')`, uidB),
		"B moved: the import records its new path and what the file changed, not a second creation")
	assert.Positive(t, historyCount(t, db, `WHERE task_uid = ? AND origin = 'import' AND field = 'title'`, uidC), "C is new and starts fresh")
}

// TestTaskHistory_ImportReplace_UIDlessTaskUnderAMovedTask_GraftsNothing (X2):
// the uid-less form. The uid-less local task is not paired with anything by the
// import, so its history stays under its id and the task now at that id starts fresh.
func TestTaskHistory_ImportReplace_UIDlessTaskUnderAMovedTask_GraftsNothing(t *testing.T) {
	uidA, uidB, uidC := taskUID(t), taskUID(t), taskUID(t)
	local, file := graftSetup(t, uidA, uidB, uidC)
	db := local.WriteDB()
	_, err := db.Exec(`UPDATE task_history SET task_uid = 'REC-1' WHERE task_uid = ?`, uidA)
	require.NoError(t, err)
	_, err = db.Exec(`UPDATE nodes SET uid = NULL WHERE id = 'REC-1'`)
	require.NoError(t, err)
	t.Setenv("MTIX_AUTHOR_ID", "importer")

	_, err = local.Import(context.Background(), file, sqlite.ImportModeReplace, true)
	require.NoError(t, err)

	assert.Equal(t, map[string]int{"REC-1": keysOfTag(t, db, "own-A")["REC-1"]}, keysOfTag(t, db, "own-A"), "the uid-less task's entries stay under its id")
	assert.Zero(t, historyCount(t, db, `WHERE task_uid IN (?, ?) AND (old_value LIKE '%alice-secret%' OR new_value LIKE '%alice-secret%')`, uidB, uidC))
}

// TestTaskHistory_ImportReplace_SameTitleDifferentIdentity_GraftsNothing (X5):
// create-time uids that differ are two tasks by the import's own identity rule
// (differentIdentity), whatever their titles.
func TestTaskHistory_ImportReplace_SameTitleDifferentIdentity_GraftsNothing(t *testing.T) {
	ctx := context.Background()
	local, mate := newTestStore(t), newTestStore(t)
	uidA, uidC := taskUID(t), taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, uidA, "Write tests")
	createSameIDTask(t, mate, "REC-1", "", 1, uidC, "Write tests")
	secret := "alice-secret"
	restore := ownedBy(t, "own-A")
	require.NoError(t, local.UpdateNode(ctx, "REC-1", &store.NodeUpdate{Assignee: &secret}))
	restore()
	t.Setenv("MTIX_AUTHOR_ID", "importer")

	_, err := local.Import(ctx, exportOf(t, mate), sqlite.ImportModeReplace, true)
	require.NoError(t, err)

	db := local.WriteDB()
	assert.Equal(t, map[string]int{uidA: keysOfTag(t, db, "own-A")[uidA]}, keysOfTag(t, db, "own-A"), "A's entries stay under A")
	assert.Zero(t, historyCount(t, db, `WHERE task_uid = ? AND (old_value LIKE '%alice-secret%' OR new_value LIKE '%alice-secret%')`, uidC))
}
