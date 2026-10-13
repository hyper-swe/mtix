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

// a merge that renumbers the local task off an id the file's task takes.
func TestTaskHistory_ImportWithLocalRenumber_PairsTasksByIdentity(t *testing.T) {
	ctx := context.Background()
	local, file, mine, _ := sameIDStores(t)
	who := "carol"
	require.NoError(t, local.UpdateNode(ctx, "REC-2", &store.NodeUpdate{Assignee: &who}))
	data := file()
	theirs := data.Nodes[1].UID
	db := local.WriteDB()
	_, err := db.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)
	_, _, err = local.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: true})
	require.NoError(t, err)
	dump := func(uid string) (n int, withOld int) {
		rows, err := db.Query(`SELECT field, origin, old_value, new_value FROM task_history WHERE task_uid = ?`, uid)
		require.NoError(t, err)
		defer rows.Close()
		for rows.Next() {
			var f, o string
			var ov, nv sql.NullString
			require.NoError(t, rows.Scan(&f, &o, &ov, &nv))
			t.Logf("%s %s %s old=%v new=%v", uid[:8], f, o, ov, nv)
			n++
			if ov.Valid {
				withOld++
			}
		}
		return
	}
	_, theirsOld := dump(theirs)
	mineN, _ := dump(mine)
	assert.Zero(t, theirsOld, "a task new to this store has no earlier value")
	assert.LessOrEqual(t, mineN, 1, "the moved local task records only its new display path")
}

// replace mode over a different task under the same id.
func TestTaskHistory_ImportReplace_DifferentTaskUnderTheSameID_LeaksNoValues(t *testing.T) {
	ctx := context.Background()
	local, file, _, _ := sameIDStores(t)
	who := "carol"
	require.NoError(t, local.UpdateNode(ctx, "REC-2", &store.NodeUpdate{Assignee: &who}))
	data := file()
	theirs := data.Nodes[1].UID
	db := local.WriteDB()
	_, err := db.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)
	_, err = local.Import(ctx, data, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
	var withOld int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND old_value IS NOT NULL`, theirs).Scan(&withOld))
	var carol int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND old_value LIKE '%carol%'`, theirs).Scan(&carol))
	t.Logf("withOld=%d carol=%d", withOld, carol)
	assert.Zero(t, carol, "the file's task never had the local task's assignee")
}
