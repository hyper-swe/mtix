// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// A merge import writes no sync journal, so on a store that syncs with a
// hub a later hub edit would silently overwrite the merged values. It is
// refused before any write.

func syncEventCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(), `SELECT count(*) FROM sync_events`).Scan(&n))
	return n
}

// storeBoard is the store's export as JSON with the export time cleared.
func storeBoard(t *testing.T) string {
	t.Helper()
	data, err := app.store.Export(context.Background(), "", "")
	require.NoError(t, err)
	data.ExportedAt = ""
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	return string(raw)
}

// backupFiles lists every file in the project's backups directory.
func backupFiles(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(app.mtixDir, "data", "backups", "*"))
	require.NoError(t, err)
	return matches
}

func TestRunImport_MergeOnSyncedStore_RefusesAndWritesNothing(t *testing.T) {
	tests := []struct {
		name string
		sync func(t *testing.T) []*model.SyncEvent
	}{
		{"events pushed to a hub", markEventsPushed},
		{"hub configured, nothing pushed yet", configureHub},
		{"hub config key only", func(t *testing.T) []*model.SyncEvent {
			_, err := app.configSvc.Set("sync.enabled", "true")
			require.NoError(t, err)
			return nil
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("MTIX_SYNC_DSN", "")
			initTestApp(t)
			seedTickets(t, 3)
			path := writeExportOf(t)
			// The file now holds a task the store lacks (TEST-3) and a title the store
			// does not have, so a merge that ran would write a task, a value and events.
			_, err := app.store.WriteDB().ExecContext(context.Background(), `DELETE FROM nodes WHERE id = 'TEST-3'`)
			require.NoError(t, err)
			_, err = app.store.WriteDB().ExecContext(context.Background(),
				`UPDATE nodes SET title = 'local title' WHERE id = 'TEST-1'`)
			require.NoError(t, err)
			tt.sync(t)
			nodes, events, board := allNodeCount(t), syncEventCount(t), storeBoard(t)

			err = runImport(path, importFlags{mode: "merge"})
			require.ErrorIs(t, err, sqlite.ErrMergeOnSyncedStore)
			assert.Contains(t, err.Error(), "a merge import writes no sync journal")
			assert.Contains(t, err.Error(), "silently overwrite the merged values")
			assert.Contains(t, err.Error(), "nothing was imported")
			assert.Contains(t, err.Error(), "mtix sync pull")
			assert.Equal(t, nodes, allNodeCount(t), "a refused merge writes no task")
			assert.Equal(t, events, syncEventCount(t), "a refused merge writes no event")
			assert.Equal(t, board, storeBoard(t), "a refused merge changes no value in the store")
			assert.Empty(t, preDestroySnapshots(t, "import-merge"), "the refusal comes before any backup")
			assert.Empty(t, backupFiles(t), "no backup of any kind is written")
		})
	}
}

func TestRunImport_MergeOnUnsyncedStore_StillMerges(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "")
	initTestApp(t)
	seedTickets(t, 3)
	path := writeExportOf(t)
	seedTickets(t, 1)
	require.Equal(t, 4, allNodeCount(t))

	require.NoError(t, runImport(path, importFlags{mode: "merge"}))
	assert.Equal(t, 4, allNodeCount(t), "a store without a hub merges as before")
}
