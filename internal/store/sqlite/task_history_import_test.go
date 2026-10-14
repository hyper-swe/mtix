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

// TestTaskHistory_Import_RecordsChangesWithOriginImport imports a file that
// changes one local task and adds another, in replace and in merge mode: each
// changed field gets one entry with origin import, a task the file leaves
// alone gets none.
func TestTaskHistory_Import_RecordsChangesWithOriginImport(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []sqlite.ImportMode{sqlite.ImportModeReplace, sqlite.ImportModeMerge} {
		t.Run(string(mode), func(t *testing.T) {
			src := newHistoryFixture(t)
			canonicalNodeTypes := func(f *historyFixture) { // the import derives node_type from depth
				_, uerr := f.db.Exec(`UPDATE nodes SET node_type = CASE depth WHEN 0 THEN 'epic' WHEN 1 THEN 'story' WHEN 2 THEN 'issue' ELSE 'micro' END`)
				require.NoError(t, uerr)
			}
			seedCascadeNodes(t, src.s, ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen), ccNode(ccOutside, model.StatusOpen))
			canonicalNodeTypes(src)
			newTitle := "retitled by the file"
			require.NoError(t, src.s.UpdateNode(ctx, ccRoot, &store.NodeUpdate{Title: &newTitle}))
			require.NoError(t, src.s.TransitionStatus(ctx, ccRoot, model.StatusInProgress, "go", "bob"))
			data, err := src.s.Export(ctx, "PROJ", "0.6.0")
			require.NoError(t, err)

			dst := newHistoryFixture(t)
			// The destination holds the same board before the file's edits, minus PROJ-2.
			seedCascadeNodes(t, dst.s, ccNode(ccRoot, model.StatusOpen), ccNode(ccChild, model.StatusOpen))
			require.NoError(t, dst.s.UpdateNode(ctx, ccRoot, &store.NodeUpdate{Title: strPtr("node " + ccRoot)}))
			canonicalNodeTypes(dst)
			dst.clearHistory(t)
			for _, id := range []string{ccRoot, ccChild} { // align uids so the nodes are the same tasks
				var uid string
				require.NoError(t, src.db.QueryRow(`SELECT uid FROM nodes WHERE id = ?`, id).Scan(&uid))
				_, err := dst.db.Exec(`UPDATE nodes SET uid = ? WHERE id = ?`, uid, id)
				require.NoError(t, err)
			}

			if mode == sqlite.ImportModeReplace {
				_, err = dst.s.Import(ctx, data, mode, false)
			} else { // the reconcile path, preferring the file's workflow values
				_, _, err = dst.s.ImportReconcile(ctx, data, sqlite.ImportReconcileOptions{
					Mode: mode, Confirm: true, Workflow: sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs}})
			}
			require.NoError(t, err)

			root := dst.fields(t, ccRoot)
			assert.Contains(t, root, "status", "the file moved PROJ-1 to in_progress")
			assert.Contains(t, root, "title", "the file retitled PROJ-1")
			assert.Empty(t, dst.fields(t, ccChild), "a task the file leaves alone has no entry")
			assert.Contains(t, dst.fields(t, ccOutside), "title", "a task the file adds gets its initial fields")
			for _, id := range []string{ccRoot, ccOutside} {
				seen := map[string]bool{}
				for _, r := range dst.rows(t, id) {
					assert.Equal(t, "import", r.origin)
					assert.Equal(t, historyEnvAuthor, r.actor)
					assert.False(t, seen[r.field], "%s: %s recorded twice", id, r.field)
					seen[r.field] = true
				}
			}
		})
	}
}
