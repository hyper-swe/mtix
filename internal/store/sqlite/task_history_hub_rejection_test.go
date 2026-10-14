// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestTaskHistory_HubRejectionRenumber_KeepsHistoryUnderOneKey renumbers a
// subtree whose history is keyed by id (a legacy, uid-less task) the way a hub rejection does: the history keyed by
// the old ids follows the tasks to their new ids.
func TestTaskHistory_HubRejectionRenumber_KeepsHistoryUnderOneKey(t *testing.T) {
	s, uid := subtreeFixture(t)
	ctx := context.Background()
	db := s.WriteDB()
	_, err := db.Exec(`UPDATE task_history SET task_uid = (SELECT id FROM nodes WHERE nodes.uid = task_history.task_uid)`)
	require.NoError(t, err)
	var rootRows, childRows int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = 'PRJX-1'`).Scan(&rootRows))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = 'PRJX-1.1'`).Scan(&childRows))
	require.Positive(t, rootRows)

	newID, err := s.RenumberForHubRejection(ctx, uid)
	require.NoError(t, err)

	var movedRoot, movedChild, stale int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ?`, newID).Scan(&movedRoot))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ?`, newID+".1").Scan(&movedChild))
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid IN ('PRJX-1', 'PRJX-1.1')`).Scan(&stale))
	assert.Equal(t, rootRows, movedRoot)
	assert.Equal(t, childRows, movedChild)
	assert.Zero(t, stale)
}

// TestTaskHistory_HubRejectionRenumber_RecordsDisplayPathAndParent pins story
// 10: each moved task records its new display path, and each moved descendant
// the parent id it now has.
func TestTaskHistory_HubRejectionRenumber_RecordsDisplayPathAndParent(t *testing.T) {
	s, uid := subtreeFixture(t)
	db := s.WriteDB()
	_, err := db.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)

	newID, err := s.RenumberForHubRejection(context.Background(), uid)
	require.NoError(t, err)

	type change struct{ field, old, new, origin string }
	got := map[string]change{}
	rows, err := db.Query(`SELECT n.id, h.field, h.old_value, h.new_value, h.origin FROM task_history h
		JOIN nodes n ON n.uid = h.task_uid WHERE h.field IN ('display_path', 'parent') ORDER BY n.id, h.field`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	for rows.Next() {
		var id string
		var c change
		require.NoError(t, rows.Scan(&id, &c.field, &c.old, &c.new, &c.origin))
		got[id+"/"+c.field] = c
	}
	require.NoError(t, rows.Err())
	assert.Equal(t, change{"display_path", `"PRJX-1"`, `"` + newID + `"`, "system"}, got[newID+"/display_path"])
	assert.Equal(t, change{"display_path", `"PRJX-1.1"`, `"` + newID + `.1"`, "system"}, got[newID+".1/display_path"])
	assert.Equal(t, change{"parent", `"PRJX-1"`, `"` + newID + `"`, "system"}, got[newID+".1/parent"])
	assert.NotContains(t, got, newID+"/parent", "the renumbered root keeps its parent")
	assert.NotContains(t, got, "PRJX-2/display_path", "an unrelated task has no entry")
}
