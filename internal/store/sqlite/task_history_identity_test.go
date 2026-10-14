// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// uidlessHistory makes the seeded tasks look like legacy tasks: no uid, and
// their history keyed by their dot-path id (MTIX-95.31.19.2 known limit).
func uidlessHistory(t *testing.T, raw *sql.DB) {
	t.Helper()
	_, err := raw.Exec(`UPDATE task_history SET task_uid = (SELECT id FROM nodes WHERE COALESCE(NULLIF(uid, ''), id) = task_history.task_uid OR uid = task_history.task_uid)`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE nodes SET uid = ''`)
	require.NoError(t, err)
}

func historyKeys(t *testing.T, raw *sql.DB) map[string]int {
	t.Helper()
	rows, err := raw.Query(`SELECT task_uid, COUNT(*) FROM task_history GROUP BY task_uid`)
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

func TestTaskHistoryRekey_Renumber_KeepsOneKeyPerTask(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	seedTree(t, s)
	uidlessHistory(t, raw)
	before := historyKeys(t, raw)
	require.Positive(t, before["MTIX-1.1"])

	require.NoError(t, s.RenumberSubtree(context.Background(), "MTIX-1", 5))

	after := historyKeys(t, raw)
	// Each moved task keeps its earlier entries and gains its display_path
	// entry; a moved descendant also gains its parent entry (MTIX-95.31.19.3).
	assert.Equal(t, before["MTIX-1"]+1, after["MTIX-5"], "the renumbered task keeps its history")
	assert.Equal(t, before["MTIX-1.1"]+2, after["MTIX-5.1"], "so do its descendants")
	assert.Equal(t, before["MTIX-1.1.1"]+2, after["MTIX-5.1.1"])
	assert.Equal(t, before["MTIX-2"], after["MTIX-2"], "an unrelated task is untouched")
	assert.Zero(t, after["MTIX-1"])
	assert.Zero(t, after["MTIX-1.1"])
}

func TestTaskHistoryRekey_UIDBackfill_MovesHistoryToTheNewUID(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	seedTree(t, s)
	uidlessHistory(t, raw)
	before := historyKeys(t, raw)

	require.NoError(t, s.BackfillUIDs(context.Background()))

	var uid string
	require.NoError(t, raw.QueryRow(`SELECT uid FROM nodes WHERE id = 'MTIX-2'`).Scan(&uid))
	require.NotEmpty(t, uid)
	after := historyKeys(t, raw)
	assert.Equal(t, before["MTIX-2"], after[uid], "history follows the task to its uid")
	assert.Zero(t, after["MTIX-2"])
}

func TestTaskHistoryRekey_ImportAs_RenamesKeysAndRecordsTheNewParent(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)
	_, err := raw.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)
	// Legacy view: the imported tasks have no uid and history keyed by id.
	_, err = raw.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value)
		VALUES ('E1', 'MTIX-1', '2026-10-13T09:00:00.000000000Z', 'a', 'local', 'change', 'title', NULL, NULL)`)
	require.NoError(t, err)

	_, err = ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)

	keys := historyKeys(t, raw)
	assert.Equal(t, 1, keys["PROJ-7.1"], "the earlier entry follows the task to its new id")
	assert.Equal(t, 1, countParentRows(t, raw, "PROJ-7.1"), "the new parent is recorded once")
	var origin, newV string
	require.NoError(t, raw.QueryRow(`SELECT h.origin, h.new_value FROM task_history h JOIN nodes n ON n.uid = h.task_uid
		WHERE n.id = 'PROJ-7.1' AND h.field = 'parent'`).Scan(&origin, &newV))
	assert.Equal(t, "import", origin)
	assert.Equal(t, `"PROJ-7"`, newV)
}

func countParentRows(t *testing.T, raw *sql.DB, id string) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history h JOIN nodes n ON n.uid = h.task_uid
		WHERE n.id = ? AND h.field = 'parent'`, id).Scan(&n))
	return n
}

func TestTaskHistoryRekey_UIDMint_MovesHistoryToTheMintedUID(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	seedTree(t, s)
	uidlessHistory(t, raw)
	_, err := raw.Exec(`DELETE FROM sync_events`) // no create event to recover the uid from: it is minted
	require.NoError(t, err)
	before := historyKeys(t, raw)

	require.NoError(t, s.BackfillUIDs(context.Background()))

	var uid string
	require.NoError(t, raw.QueryRow(`SELECT uid FROM nodes WHERE id = 'MTIX-2'`).Scan(&uid))
	require.NotEmpty(t, uid)
	after := historyKeys(t, raw)
	assert.Equal(t, before["MTIX-2"], after[uid])
	assert.Zero(t, after["MTIX-2"])
}

// TestTaskHistoryRekey_ImportAs_RecordsEveryRenamedTaskAndItsParent: ImportAs
// is the same identity rewrite as a renumber and records the same two fields:
// a display_path entry per renamed task, and a parent entry per renamed
// descendant (a former root also records its new parent).
func TestTaskHistoryRekey_ImportAs_RecordsEveryRenamedTaskAndItsParent(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)
	_, err := raw.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)

	_, err = ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)

	rows, err := raw.Query(`SELECT n.id, h.field, h.old_value, h.new_value, h.origin FROM task_history h
		JOIN nodes n ON COALESCE(NULLIF(n.uid, ''), n.id) = h.task_uid
		WHERE h.field IN ('display_path', 'parent') ORDER BY n.id, h.field`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	got := map[string][2]string{}
	for rows.Next() {
		var id, field, origin string
		var oldV, newV sql.NullString
		require.NoError(t, rows.Scan(&id, &field, &oldV, &newV, &origin))
		assert.Equal(t, "import", origin)
		got[id+"/"+field] = [2]string{oldV.String, newV.String}
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, [2]string{`"MTIX-1"`, `"PROJ-7.1"`}, got["PROJ-7.1/display_path"])
	assert.Equal(t, [2]string{`"MTIX-1.1"`, `"PROJ-7.1.1"`}, got["PROJ-7.1.1/display_path"])
	assert.Equal(t, [2]string{`"MTIX-1.1"`, `"PROJ-7.1.1"`}, got["PROJ-7.1.1/display_path"])
	assert.Equal(t, [2]string{"null", `"PROJ-7"`}, got["PROJ-7.1/parent"], "a former root gains its new parent")
	assert.Equal(t, [2]string{`"MTIX-1"`, `"PROJ-7.1"`}, got["PROJ-7.1.1/parent"], "a descendant's parent id follows the rename")
	assert.Equal(t, [2]string{`"MTIX-1.1"`, `"PROJ-7.1.1"`}, got["PROJ-7.1.1.1/parent"], "so does a grandchild's")
	assert.Equal(t, [2]string{`"MTIX-2"`, `"PROJ-7.2"`}, got["PROJ-7.2/display_path"])
}

// TestTaskHistoryRekey_ImportAs_UIDlessTasks_PairByTheirOldIDs: a legacy task
// has no uid to pair its old and new state, so the rename mapping does.
func TestTaskHistoryRekey_ImportAs_UIDlessTasks_PairByTheirOldIDs(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedParentForImport(t, s, "PROJ-7")
	seedTree(t, s)
	_, err := raw.Exec(`UPDATE nodes SET uid = '' WHERE id LIKE 'MTIX-%'`)
	require.NoError(t, err)
	_, err = raw.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)

	_, err = ImportAs(context.Background(), s, mtixDir, "PROJ-7")
	require.NoError(t, err)

	var path, parentRows, freshNew int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE field = 'display_path' AND task_uid LIKE 'PROJ-7.%'`).Scan(&path))
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE field = 'parent' AND task_uid LIKE 'PROJ-7.%'`).Scan(&parentRows))
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE field = 'title'`).Scan(&freshNew))
	assert.Equal(t, 5, path, "a display_path entry per renamed task")
	assert.Equal(t, 5, parentRows, "a parent entry per renamed task: former roots gain a parent, descendants follow the rename")
	assert.Zero(t, freshNew, "no task is treated as new")
}

// TestTaskHistoryInsert_OnlyTheReplayConflictIsIgnored runs the append
// statement itself: a repeat of one event's entry is ignored, and any other
// constraint violation (here a repeated primary key) still fails the write.
func TestTaskHistoryInsert_OnlyTheReplayConflictIsIgnored(t *testing.T) {
	s, raw, _ := reconcileTestStore(t)
	_ = raw
	insert := func(tx *sql.Tx, entry, event string) error {
		var ev any
		if event != "" {
			ev = event
		}
		_, err := tx.ExecContext(context.Background(), historyAppendSQL, entry, "K", "2026-10-13T09:00:00.000000000Z",
			"a", "local", "change", "status", nil, nil, ev)
		return err
	}
	require.NoError(t, s.WithTx(context.Background(), func(tx *sql.Tx) error {
		require.NoError(t, insert(tx, "e1", "EV1"))
		require.NoError(t, insert(tx, "e2", "EV1"), "the same event and field again is ignored")
		assert.Error(t, insert(tx, "e1", ""), "a repeated entry id is a real error")
		return nil
	}))
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = 'K'`).Scan(&n))
	assert.Equal(t, 1, n)
}

// TestTaskHistoryRekey_RenameTo_RecordsEveryRenamedTask: a project-prefix
// rename is an identity rewrite like a renumber and records the same fields.
func TestTaskHistoryRekey_RenameTo_RecordsEveryRenamedTask(t *testing.T) {
	s, raw, mtixDir := reconcileTestStore(t)
	seedTree(t, s)
	_, err := raw.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)

	_, err = RenameTo(context.Background(), s, mtixDir, "DEMO")
	require.NoError(t, err)

	var paths, parents int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE field = 'display_path' AND origin = 'local'`).Scan(&paths))
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE field = 'parent' AND origin = 'local'`).Scan(&parents))
	assert.Equal(t, 5, paths, "a display_path entry per renamed task")
	assert.Equal(t, 3, parents, "a parent entry per renamed descendant")
	assert.Zero(t, countTitles(t, raw), "no task is treated as new")
}

func countTitles(t *testing.T, raw *sql.DB) int {
	t.Helper()
	var n int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM task_history WHERE field = 'title'`).Scan(&n))
	return n
}
