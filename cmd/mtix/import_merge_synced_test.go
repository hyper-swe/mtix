// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
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
			seedTickets(t, 2)
			path := writeExportOf(t)
			seedTickets(t, 1) // a ticket the file lacks, so the store is not a plain copy
			tt.sync(t)
			nodes, events := allNodeCount(t), syncEventCount(t)

			err := runImport(path, importFlags{mode: "merge"})
			require.ErrorIs(t, err, sqlite.ErrMergeOnSyncedStore)
			assert.Contains(t, err.Error(), "a merge import writes no sync journal")
			assert.Contains(t, err.Error(), "silently overwrite the merged values")
			assert.Contains(t, err.Error(), "nothing was imported")
			assert.Contains(t, err.Error(), "mtix sync pull")
			assert.Equal(t, nodes, allNodeCount(t), "a refused merge writes no task")
			assert.Equal(t, events, syncEventCount(t), "a refused merge writes no event")
			assert.Empty(t, preDestroySnapshots(t, "import-merge"), "the refusal comes before any backup")
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
