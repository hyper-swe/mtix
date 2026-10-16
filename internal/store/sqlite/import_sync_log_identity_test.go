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
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-107.31: the uid-first refusal also holds for a task whose creation is
// still pending, and for a task pulled from a teammate; and a store that
// syncs still takes the backfill-uid keep and adopt flows (MTIX-95.31.9,
// MTIX-95.31.16).

const swappedUID = "01900000-0000-7000-8000-000000000def"

// markSynced gives the store the hub-connected mark, with nothing pushed.
func markSynced(t *testing.T, s *sqlite.Store) {
	t.Helper()
	_, err := s.MarkHubConnected(context.Background(), "config")
	require.NoError(t, err)
}

func TestImport_ReplaceOnSyncedStore_RefusesUIDSwapOfPendingAndPulledTasks(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, s *sqlite.Store, raw *sql.DB)
		edit  func(n *sqlite.ExportData, i int)
		id    string
	}{
		{"pending creation, uid dropped", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-2", "")
			markSynced(t, s)
		}, func(d *sqlite.ExportData, i int) { d.Nodes[i].UID = "" }, "MTIX-2"},
		{"pending creation, other title and other uid", func(t *testing.T, s *sqlite.Store, _ *sql.DB) {
			mustCreateNode(t, s, "MTIX-2", "")
			markSynced(t, s)
		}, func(d *sqlite.ExportData, i int) {
			d.Nodes[i].UID, d.Nodes[i].Title = swappedUID, "Another task entirely"
		}, "MTIX-2"},
		{"pulled task, uid changed", func(t *testing.T, s *sqlite.Store, raw *sql.DB) {
			pullEvents(t, s, teammateCreateEvents(t, "MTIX-3"))
			require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM sync_events
				WHERE node_id = 'MTIX-3' AND op_type = 'create_node' AND sync_status = 'applied'`),
				"the pulled creation is held as applied")
		}, func(d *sqlite.ExportData, i int) { d.Nodes[i].UID = swappedUID }, "MTIX-3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			tt.setup(t, s, raw)
			uid := nodeUID(t, raw, tt.id)
			require.NotEmpty(t, uid)
			board, err := s.Export(ctx, "", "")
			require.NoError(t, err)
			for i := range board.Nodes {
				if board.Nodes[i].ID == tt.id {
					tt.edit(board, i)
				}
			}
			require.NoError(t, sqlite.RecomputeExportChecksum(board))

			_, err = s.Import(ctx, board, sqlite.ImportModeReplace, false)
			require.ErrorIs(t, err, sqlite.ErrReplaceOnSyncedStore)
			assert.Equal(t, uid, nodeUID(t, raw, tt.id), "a refused replace changes nothing")
			missing, err := s.MissingLoggedNodes(ctx)
			require.NoError(t, err)
			assert.Empty(t, missing)
		})
	}
}

// A board written before uids were shared replaces a marked backfill uid,
// which the hub never knew the task by, so a store that syncs imports it:
// a file copy without a uid keeps the local backfill uid, and a file copy
// with the hub's uid adopts it.
func TestImport_ReplaceOnSyncedStore_BackfillUIDKeepAndAdopt_Imports(t *testing.T) {
	tests := []struct {
		name string
		// fileUID is the uid the file holds REC-1 under.
		fileUID func(t *testing.T, local string) string
		// pushed marks the creation of REC-1 as pushed to the hub.
		pushed   bool
		wantKept bool
	}{
		{"file copy without a uid keeps the backfill uid", func(*testing.T, string) string { return "" }, false, true},
		{"file copy without a uid, creation pushed", func(*testing.T, string) string { return "" }, true, true},
		{"file copy with the hub's uid, creation pushed", func(t *testing.T, _ string) string { return taskUID(t) }, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			local := newTestStore(t)
			uid := createSameIDTask(t, local, "REC-1", "", 1, importBackfillUID(t), "Shared task")
			markSynced(t, local)
			if tt.pushed {
				_, err := local.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed'`)
				require.NoError(t, err)
			}
			require.NotZero(t, countLoggedEvents(t, local, "REC-1"), "the sync log holds an event of REC-1")
			file := exportOf(t, local)
			file.Nodes[0].UID = tt.fileUID(t, uid)
			require.NoError(t, sqlite.RecomputeExportChecksum(file))

			_, err := local.Import(ctx, file, sqlite.ImportModeReplace, false)
			require.NoError(t, err, "the replace is not refused")
			got := uidAtID(t, local, "REC-1")
			assert.Equal(t, tt.wantKept, got == uid)
			assert.True(t, model.IsBackfillUID(got) || !tt.wantKept)
		})
	}
}

// countLoggedEvents counts the sync events of node id.
func countLoggedEvents(t *testing.T, s *sqlite.Store, id string) int {
	t.Helper()
	var n int
	require.NoError(t, s.WriteDB().QueryRow(`SELECT COUNT(*) FROM sync_events WHERE node_id = ?`, id).Scan(&n))
	return n
}
