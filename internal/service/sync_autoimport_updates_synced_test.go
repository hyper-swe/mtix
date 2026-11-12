// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// An automatic import writes no sync journal. On a store that syncs with a
// hub, a file that changes a task both hold is refused: a later hub edit
// would silently overwrite the imported value. A file that only adds tasks
// still imports, and a store that does not sync imports the change.

func retitle(d *sqlite.ExportData, id, title string) {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			d.Nodes[i].Title = title
		}
	}
}

func TestAutoImport_FileUpdatesATaskOnSyncedStore_IsRefusedOnceAndWritesNothing(t *testing.T) {
	ctx := context.Background()
	a := newGuardFixture(t)
	createTask(t, a, "PROJ-3", 3)
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	pushAll(t, a)
	board := a.teammateBoard(t, func(d *sqlite.ExportData) { retitle(d, "PROJ-3", "Teammate retitle") })
	a.pull(t, board)
	snapshot := a.storeSnapshot(t)

	err := a.svc.AutoImport(ctx, a.mtixDir)
	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	got, getErr := a.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, getErr)
	assert.Equal(t, "Task PROJ-3", got.Title, "nothing was imported")
	assert.Equal(t, snapshot, a.storeSnapshot(t), "the store is unchanged")
	assert.Empty(t, a.preSyncBackups(t), "a refused import takes no pre-sync backup")
	assert.Equal(t, string(board), string(a.read(t, "tasks.json")), "the pulled file is left as it is")
	for _, want := range []string{"PROJ-3", "this store syncs with a hub", "writes no sync journal",
		"silently overwrite", "Nothing was imported", "mtix sync pull"} {
		assert.Contains(t, a.notices.String(), want)
	}
	assert.NotContains(t, a.notices.String(), "--mode merge", "a merge import is refused on a store that syncs")
	var rec service.AutoImportRefusal
	require.NoError(t, json.Unmarshal(a.read(t, "data/auto-import-refusal.json"), &rec))
	assert.Equal(t, "merge_synced", rec.Kind)
	assert.True(t, rec.Synced)
	assert.Contains(t, rec.Reason, "PROJ-3")

	recorded := a.read(t, "data/auto-import-refusal.json")
	a.now = a.now.Add(time.Hour)

	// The same command again in the same process repeats the error, not the text.
	first := strings.Count(a.notices.String(), "auto-import of .mtix/tasks.json refused")
	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
	assert.Equal(t, first, strings.Count(a.notices.String(), "auto-import of .mtix/tasks.json refused"),
		"the refusal text is printed once per process")
	assert.Equal(t, string(recorded), string(a.read(t, "data/auto-import-refusal.json")),
		"the same refusal is not recorded again")
}

func TestAutoImport_FileUpdatesATaskOnUnsyncedStore_Imports(t *testing.T) {
	ctx := context.Background()
	a := newGuardFixture(t)
	a.pull(t, a.teammateBoard(t, func(d *sqlite.ExportData) { retitle(d, "PROJ-2", "Teammate retitle") }))

	require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir), a.notices.String())
	got, err := a.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, "Teammate retitle", got.Title)
}
