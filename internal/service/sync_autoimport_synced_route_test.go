// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The refusal of a file that changes a held task on a store that syncs names
// the routes that end it. These tests run both and the one the old text
// offered first: a pull alone does not end the refusal, mtix sync --fix
// (ForceExport) does, with or without the pull before it.

func refusedSyncedStore(t *testing.T) (*guardFixture, []byte) {
	t.Helper()
	a := newGuardFixture(t)
	createTask(t, a, "PROJ-3", 3)
	require.NoError(t, a.svc.AutoExport(context.Background(), a.mtixDir))
	pushAll(t, a)
	board := a.teammateBoard(t, func(d *sqlite.ExportData) { retitle(d, "PROJ-3", "Teammate retitle") })
	a.pull(t, board)
	require.ErrorIs(t, a.svc.AutoImport(context.Background(), a.mtixDir), service.ErrAutoImportRefused)
	return a, board
}

func pendingRefusal(t *testing.T, a *guardFixture) service.AutoImportRefusal {
	t.Helper()
	var rec service.AutoImportRefusal
	require.NoError(t, json.Unmarshal(a.read(t, "data/auto-import-refusal.json"), &rec))
	return rec
}

func TestAutoImport_SyncedRefusal_PullAloneKeepsItPending_FixEndsIt(t *testing.T) {
	ctx := context.Background()
	a, board := refusedSyncedStore(t)

	// mtix sync pull applies the teammate's hub edit to the store.
	a.exec(t, `UPDATE nodes SET title = 'Teammate retitle' WHERE id = 'PROJ-3'`)
	require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	rec := pendingRefusal(t, a)
	assert.Empty(t, rec.ResolvedAt, "after a pull alone the refusal is still pending")
	assert.Equal(t, "conflict", rec.Kind, "and it now reads as a conflict")
	assert.Equal(t, string(board), string(a.read(t, "tasks.json")), "the file is still the pulled one")

	// mtix sync --fix rewrites the file from the store and ends it.
	require.NoError(t, a.svc.ForceExport(ctx, a.mtixDir))
	assert.NotEmpty(t, pendingRefusal(t, a).ResolvedAt)
	require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	got, err := a.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err)
	assert.Equal(t, "Teammate retitle", got.Title)
}

func TestAutoImport_SyncedRefusal_FixAloneEndsIt(t *testing.T) {
	ctx := context.Background()
	a, _ := refusedSyncedStore(t)
	snapshot := a.storeSnapshot(t)

	require.NoError(t, a.svc.ForceExport(ctx, a.mtixDir))
	assert.NotEmpty(t, pendingRefusal(t, a).ResolvedAt)
	require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir))
	assert.Equal(t, snapshot, a.storeSnapshot(t), "the local store is kept")
	assert.NotContains(t, string(a.read(t, "tasks.json")), "Teammate retitle", "the file's change is dropped")
}

func TestAutoImport_SyncedRefusal_MessageNamesTheRouteThatEndsIt(t *testing.T) {
	a, _ := refusedSyncedStore(t)
	notice := a.notices.String()

	assert.Contains(t, notice, "mtix sync --fix")
	assert.Contains(t, notice, "a pull alone does not end this refusal", "the pull advice says it is not enough")
	assert.Contains(t, notice, "mtix sync pull")
	first, pull := strings.Index(notice, "mtix sync --fix"), strings.Index(notice, "mtix sync pull")
	assert.Less(t, first, pull, "the step that ends the refusal comes first")
	assert.Contains(t, notice, "then mtix sync --fix")
}

// A lossy or conflict refusal on a store that syncs offers the hub's board
// the same way: a pull alone leaves it pending, and mtix sync --fix ends it.
func TestAutoImport_SyncedLossyRefusal_NamesPullThenFix_AndFixEndsIt(t *testing.T) {
	ctx := context.Background()
	a := newGuardFixture(t)
	older := a.read(t, "tasks.json")
	createTask(t, a, "PROJ-3", 3)
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	pushAll(t, a)
	a.pull(t, older) // an older board that lacks the pushed task

	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
	notice := a.notices.String()
	assert.Contains(t, notice, "mtix sync pull, then mtix sync --fix")
	assert.Contains(t, notice, "a pull alone leaves it pending")

	// mtix sync pull changes the store; the refusal is still pending.
	a.exec(t, `UPDATE nodes SET title = 'Pulled retitle' WHERE id = 'PROJ-3'`)
	_ = a.svc.AutoImport(ctx, a.mtixDir)
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	assert.Empty(t, pendingRefusal(t, a).ResolvedAt, "after a pull alone the refusal is still pending")
	assert.Equal(t, string(older), string(a.read(t, "tasks.json")), "the file is still the older board")

	require.NoError(t, a.svc.ForceExport(ctx, a.mtixDir))
	assert.NotEmpty(t, pendingRefusal(t, a).ResolvedAt)
	require.NoError(t, a.svc.AutoImport(ctx, a.mtixDir))
	assert.Contains(t, string(a.read(t, "tasks.json")), "Pulled retitle", "the file is rewritten from the store")
}
