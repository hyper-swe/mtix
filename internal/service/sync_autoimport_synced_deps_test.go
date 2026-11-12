// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// An automatic import writes no sync journal. On a store that syncs, a file
// that adds a dependency with an end the store already holds changes a task
// no peer will hear about, so it is refused like a file that retitles one. A
// dependency between tasks the file itself adds comes with those tasks.

func syncedDepsFixture(t *testing.T) *guardFixture {
	t.Helper()
	a := newGuardFixture(t)
	createTask(t, a, "PROJ-3", 3)
	require.NoError(t, a.svc.AutoExport(context.Background(), a.mtixDir))
	pushAll(t, a)
	return a
}

func TestAutoImport_FileAddsADependencyOnSyncedStore_IsRefusedAndWritesNothing(t *testing.T) {
	tests := []struct {
		name  string
		edit  func(t *testing.T, d *sqlite.ExportData)
		names []string
	}{
		{"between two held tasks", func(t *testing.T, d *sqlite.ExportData) {
			addBoardDependency(t, d, "PROJ-2", "PROJ-3", "related")
		}, []string{"PROJ-2", "PROJ-3", "related"}},
		{"from a new task to a held one", func(t *testing.T, d *sqlite.ExportData) {
			addTeammateNode(t, d, "PROJ-2", "PROJ-4", 4)
			addBoardDependency(t, d, "PROJ-4", "PROJ-3", "blocks")
		}, []string{"PROJ-4", "PROJ-3", "blocks"}},
		{"from a held task to a new one", func(t *testing.T, d *sqlite.ExportData) {
			addTeammateNode(t, d, "PROJ-2", "PROJ-4", 4)
			addBoardDependency(t, d, "PROJ-3", "PROJ-4", "blocks")
		}, []string{"PROJ-3", "PROJ-4", "blocks"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			a := syncedDepsFixture(t)
			board := a.teammateBoard(t, func(d *sqlite.ExportData) { tt.edit(t, d) })
			a.pull(t, board)
			snapshot := a.storeSnapshot(t)
			var events int
			require.NoError(t, a.store.QueryRow(ctx, `SELECT count(*) FROM sync_events`).Scan(&events))

			err := a.svc.AutoImport(ctx, a.mtixDir)
			require.ErrorIs(t, err, service.ErrAutoImportRefused)
			assert.Equal(t, snapshot, a.storeSnapshot(t), "the store is unchanged, no link was written")
			var after int
			require.NoError(t, a.store.QueryRow(ctx, `SELECT count(*) FROM sync_events`).Scan(&after))
			assert.Equal(t, events, after)
			assert.Equal(t, string(board), string(a.read(t, "tasks.json")), "the pulled file is left as it is")
			assert.Empty(t, a.preSyncBackups(t), "a refusal takes no backup")
			for _, want := range tt.names {
				assert.Contains(t, a.notices.String(), want)
			}
			assert.Contains(t, a.notices.String(), "dependenc")
			var rec service.AutoImportRefusal
			require.NoError(t, json.Unmarshal(a.read(t, "data/auto-import-refusal.json"), &rec))
			assert.Equal(t, "merge_synced", rec.Kind)
		})
	}
}

func TestAutoImport_FileAddsTasksAndALinkBetweenThemOnSyncedStore_Imports(t *testing.T) {
	ctx := context.Background()
	a := syncedDepsFixture(t)
	a.pull(t, a.teammateBoard(t, func(d *sqlite.ExportData) {
		addTeammateNode(t, d, "PROJ-2", "PROJ-4", 4)
		addTeammateNode(t, d, "PROJ-2", "PROJ-5", 5)
		addBoardDependency(t, d, "PROJ-4", "PROJ-5", "related")
	}))

	require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir), a.notices.String())
	_, err := a.store.GetNode(ctx, "PROJ-5")
	require.NoError(t, err, "the added tasks are imported")
}

func TestAutoImport_FileAddsADependencyOnUnsyncedStore_Imports(t *testing.T) {
	ctx := context.Background()
	a := newGuardFixture(t)
	a.pull(t, a.teammateBoard(t, func(d *sqlite.ExportData) { addBoardDependency(t, d, "PROJ-1", "PROJ-2", "related") }))

	require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir), a.notices.String())
	var n int
	require.NoError(t, a.store.QueryRow(ctx, `SELECT count(*) FROM dependencies WHERE from_id = 'PROJ-1' AND to_id = 'PROJ-2'`).Scan(&n))
	assert.Equal(t, 1, n, "a store that does not sync takes the link")
}
