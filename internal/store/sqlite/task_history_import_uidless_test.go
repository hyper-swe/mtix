// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// A re-import of the store's own export changes nothing and must record nothing.
func TestTaskHistory_NoOpImportRoundTrip_RecordsNothing(t *testing.T) {
	for _, mode := range []sqlite.ImportMode{sqlite.ImportModeReplace, sqlite.ImportModeMerge} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			f := newHistoryFixture(t)
			seedCascadeNodes(t, f.s, ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusInProgress), ccNode(ccChild2, model.StatusOpen))
			require.NoError(t, f.s.AddDependency(ctx, &model.Dependency{FromID: ccChild, ToID: ccChild2, DepType: model.DepTypeBlocks, CreatedAt: historyTestTime}))
			require.NoError(t, f.s.SetAnnotations(ctx, ccRoot, []model.Annotation{{ID: "01J9SAMEID0000000000000009", Author: "r", Text: "x", CreatedAt: historyTestTime}}))
			_, err := f.db.Exec(`UPDATE nodes SET node_type = CASE depth WHEN 0 THEN 'epic' WHEN 1 THEN 'story' WHEN 2 THEN 'issue' ELSE 'micro' END`)
			require.NoError(t, err) // the import derives node_type from depth
			data, err := f.s.Export(ctx, "PROJ", "0.6.0")
			require.NoError(t, err)
			f.clearHistory(t)
			_, err = f.s.Import(ctx, data, mode, true)
			require.NoError(t, err)
			var n int
			require.NoError(t, f.db.QueryRow(`SELECT COUNT(*) FROM task_history`).Scan(&n))
			assert.Zero(t, n, "a no-op import records nothing")
		})
	}
}

// Replace over a uid-less local task under the same id as a different file task.
func TestTaskHistory_ImportReplace_OverAUIDlessLocalTask_LeaksNoValues(t *testing.T) {
	ctx := context.Background()
	local, file, _, _ := sameIDStores(t)
	who := "carol"
	require.NoError(t, local.UpdateNode(ctx, "REC-2", &store.NodeUpdate{Assignee: &who}))
	data := file()
	theirs := data.Nodes[1].UID
	db := local.WriteDB()
	_, err := db.Exec(`UPDATE nodes SET uid = NULL WHERE id = 'REC-2'`)
	require.NoError(t, err)
	_, err = db.Exec(`DELETE FROM task_history`)
	require.NoError(t, err)
	_, err = local.Import(ctx, data, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
	var carol int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE task_uid = ? AND old_value LIKE '%carol%'`, theirs).Scan(&carol))
	assert.Zero(t, carol, "the file's task never had the uid-less local task's assignee")
}
