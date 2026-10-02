// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestImport_FileGivesTwoNodesOneUID_RejectedAndNothingWritten verifies an
// import whose result would leave two nodes with one uid, or that adopts a
// uid another local task holds, fails whole and leaves the store as it was
// (MTIX-95.31.8). Whether the import's own checks or the unique index
// rejects it is not pinned here: the index itself is pinned by
// TestNodesUID_DuplicateInsertOrUpdate_IsRejected.
func TestImport_FileGivesTwoNodesOneUID_RejectedAndNothingWritten(t *testing.T) {
	tests := []struct {
		name  string
		mode  sqlite.ImportMode
		setup func(t *testing.T, local, file *sqlite.Store)
	}{
		{"merge adopts a uid another local task holds", sqlite.ImportModeMerge,
			func(t *testing.T, local, file *sqlite.Store) {
				a, b := backfilledUID(t), backfilledUID(t)
				createSameIDTask(t, local, "REC-1", "", 1, a, "Shared task")
				createSameIDTask(t, local, "REC-2", "", 2, b, "Local only")
				createSameIDTask(t, file, "REC-1", "", 1, b, "Shared task")
			}},
		{"replace of a file whose two nodes share a uid", sqlite.ImportModeReplace,
			func(t *testing.T, local, file *sqlite.Store) {
				createSameIDTask(t, local, "REC-1", "", 1, backfilledUID(t), "Local task")
				u := backfilledUID(t)
				createSameIDTask(t, file, "REC-1", "", 1, u, "File one")
				createSameIDTask(t, file, "REC-2", "", 2, backfilledUID(t), "File two")
				_, err := file.WriteDB().ExecContext(context.Background(),
					`DROP INDEX idx_nodes_uid`)
				require.NoError(t, err)
				_, err = file.WriteDB().ExecContext(context.Background(), `UPDATE nodes SET uid = ?`, u)
				require.NoError(t, err)
			}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			local, file := newTestStore(t), newTestStore(t)
			tt.setup(t, local, file)
			before := storeSnapshotJSON(t, local)

			_, _, err := local.ImportReconcile(ctx, exportOf(t, file), sqlite.ImportReconcileOptions{
				Mode: tt.mode, Confirm: true,
			})
			require.Error(t, err)
			assert.Equal(t, before, storeSnapshotJSON(t, local), "a failed import writes nothing")
		})
	}
}
