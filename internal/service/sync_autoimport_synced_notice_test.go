// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-107.31: a replace import is refused on a project that syncs with a
// hub, so no text of the automatic import's refusal offers it there. Every
// text names the rebuild from the hub instead (sqlite.SyncLogRecovery).

// markFixtureSynced gives f's store the hub-connected mark.
func markFixtureSynced(t *testing.T, f *guardFixture) {
	t.Helper()
	_, err := f.store.MarkHubConnected(context.Background(), "config")
	require.NoError(t, err)
}

// assertNoReplaceOffer checks text offers the rebuild from the hub and not
// the replace import.
func assertNoReplaceOffer(t *testing.T, text, where string) {
	t.Helper()
	assert.Contains(t, text, sqlite.SyncLogRecovery, where)
	assert.NotContains(t, text, "--mode replace", where)
}

// A refusal of a changed tasks.json when both sides changed and a replace
// would lose data (the conflict path), and the line the next write prints
// while it is pending, offer the rebuild on a store that syncs.
func TestAutoImport_SyncedStoreConflictRefusal_OffersRebuildNotReplace(t *testing.T) {
	ctx := context.Background()
	f, _ := refusedFixture(t)
	markFixtureSynced(t, f)
	writeLocalTask(t, f, "PROJ-4", 4)

	require.ErrorIs(t, f.svc.AutoImport(ctx, f.mtixDir), service.ErrAutoImportRefused)
	assertNoReplaceOffer(t, f.notices.String(), "the conflict refusal")
	assert.Contains(t, f.notices.String(), "PROJ-4: the whole node", "it still names what would be lost")

	f.notices.Reset()
	writeLocalTask(t, f, "PROJ-5", 5)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	assertNoReplaceOffer(t, f.notices.String(), "the line a write prints while the conflict is pending")
}

// The same for the lossy refusal and its pending line.
func TestAutoImport_SyncedStoreLossyRefusal_OffersRebuildNotReplace(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	markFixtureSynced(t, f)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil })
	f.pull(t, board)

	require.ErrorIs(t, f.svc.AutoImport(ctx, f.mtixDir), service.ErrAutoImportRefused)
	assertNoReplaceOffer(t, f.notices.String(), "the lossy refusal")

	f.notices.Reset()
	writeLocalTask(t, f, "PROJ-4", 4)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	assertNoReplaceOffer(t, f.notices.String(), "the line a write prints while the refusal is pending")
}

// A conflict that loses nothing logs its ways out; on a store that syncs
// the replace import is not one of them.
func TestAutoImport_SyncedStoreLosslessConflict_LogsRebuildNotReplace(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	var logs bytes.Buffer
	f.svc = service.NewSyncService(f.store, slog.New(slog.NewTextHandler(&logs, nil)),
		func() time.Time { return f.now })
	f.svc.SetNoticeWriter(f.notices)
	markFixtureSynced(t, f)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
	title := "Local edit after the last export"
	require.NoError(t, f.store.UpdateNode(ctx, "PROJ-2", &store.NodeUpdate{Title: &title}))
	f.pull(t, board)

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	warn := lineWith(logs.String(), "conflict detected")
	require.NotEmpty(t, warn)
	assert.Contains(t, warn, "rebuild this store from the hub")
	assert.Contains(t, warn, sqlite.SyncLogRecovery)
	assert.NotContains(t, warn, "--mode replace")

	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.Equal(t, "conflict", report.AutoImport.LastRefusal.Kind)
	assert.True(t, report.AutoImport.LastRefusal.Synced, "the record keeps that the store syncs")
}

// When the store cannot say whether it syncs, the lossy auto-import fails
// closed: it returns the wrapped error, imports nothing, and keeps the file.
func TestAutoImport_StoreSyncsUnreadable_FailsClosedAndKeepsTheFile(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil })
	f.pull(t, board)
	before := f.storeSnapshot(t)
	// The check reads applied_events; without the table it cannot be answered.
	_, err := f.store.WriteDB().ExecContext(ctx, `DROP TABLE applied_events`)
	require.NoError(t, err)

	err = f.svc.AutoImport(ctx, f.mtixDir)
	require.Error(t, err)
	assert.NotErrorIs(t, err, service.ErrAutoImportRefused, "the failure is not reported as a refusal")
	assert.Contains(t, err.Error(), "read whether the store syncs with a hub")
	assert.Equal(t, before, f.storeSnapshot(t), "nothing was imported")
	assert.Equal(t, string(board), string(f.read(t, "tasks.json")), "the file is kept")
}
