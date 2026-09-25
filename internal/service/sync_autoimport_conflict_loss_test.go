// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.4 (FR-15.2h, FR-15.2i): a conflict (both the local
// store and tasks.json changed since the last sync) keeps the loss list
// when a replace of the file would also delete local data, and names what
// a replace would delete; and a replace import writes nothing when the
// store changed after the loss check, so a concurrent write survives and
// the next command runs the guard again. Written red-first against the
// MTIX-95.31.2 round-3 code.
package service_test

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// lineWith returns the first line of text that contains substr, or "".
func lineWith(text, substr string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, substr) {
			return line
		}
	}
	return ""
}

// TestAutoImport_LocalWriteAfterLossyRefusal_ConflictKeepsLossList verifies
// the loss list survives a local write after a lossy refusal (the refusal
// says writes are fine): the next command records a conflict that still
// names the loss, prints it, and the conflict's replace option names what
// a replace would delete.
func TestAutoImport_LocalWriteAfterLossyRefusal_ConflictKeepsLossList(t *testing.T) {
	ctx := context.Background()
	f, board := refusedFixture(t)
	writeLocalTask(t, f, "PROJ-4", 4)

	err := f.svc.AutoImport(ctx, f.mtixDir) // the next command
	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	msg := f.notices.String()
	assert.Contains(t, msg, refusalHeader+": both the local store and the file changed since the last sync")
	assert.Contains(t, msg, "  PROJ-1: 2 annotations (01J9GITPULL000000000000001, 01J9GITPULL000000000000002)\n")
	assert.Contains(t, msg, "  PROJ-4: the whole node\n")
	assert.Contains(t, lineWith(msg, "mtix import .mtix/tasks.json --mode replace"), "delete the local data listed above")
	assert.NotContains(t, msg, "renumbered", "no local task is a different task under a pulled id")

	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	refusal := report.AutoImport.LastRefusal
	require.NotNil(t, refusal)
	assert.Equal(t, "conflict", refusal.Kind)
	assert.True(t, refusal.Pending)
	assert.Contains(t, refusal.Reason, "changed since the last sync")
	assert.Contains(t, refusal.Reason, "PROJ-1: 2 annotations")
	assert.Contains(t, refusal.Reason, "PROJ-4: the whole node")

	f.notices.Reset()
	writeLocalTask(t, f, "PROJ-5", 5)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir)) // the write's export keeps the board
	assert.Equal(t, string(board), string(f.read(t, "tasks.json")))
	assert.Contains(t, f.notices.String(), "keep the file with mtix import .mtix/tasks.json --mode replace, "+
		"which deletes PROJ-1: 2 annotations (01J9GITPULL000000000000001, 01J9GITPULL000000000000002); "+
		"PROJ-4: the whole node")
}

// TestAutoImport_LosslessConflict_PrintsNothingAndReturnsNil verifies a
// conflict whose replace would lose nothing (a local retitle) is recorded
// without a refusal message, and AutoImport returns nil.
func TestAutoImport_LosslessConflict_PrintsNothingAndReturnsNil(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
	title := "Local edit after the last export"
	require.NoError(t, f.store.UpdateNode(ctx, "PROJ-2", &store.NodeUpdate{Title: &title}))
	f.pull(t, board)

	require.NoError(t, f.svc.AutoImport(ctx, f.mtixDir))
	assert.Empty(t, f.notices.String(), "a lossless conflict prints no refusal")
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.Equal(t, "conflict", report.AutoImport.LastRefusal.Kind)
	assert.Empty(t, report.AutoImport.LastRefusal.Loss)
}

// TestAutoExport_LossyRefusalPending_LineNamesTheLoss verifies the line a
// write prints while a lossy refusal is pending names what the replace
// option deletes.
func TestAutoExport_LossyRefusalPending_LineNamesTheLoss(t *testing.T) {
	f, _ := refusedFixture(t)
	writeLocalTask(t, f, "PROJ-4", 4)

	require.NoError(t, f.svc.AutoExport(context.Background(), f.mtixDir))
	assert.Contains(t, f.notices.String(), "mtix import .mtix/tasks.json --mode replace, "+
		"which deletes PROJ-1: 2 annotations (01J9GITPULL000000000000001, 01J9GITPULL000000000000002)")
}

// writeOnTrigger is a log handler that runs write once, when the service
// logs sync_import_triggered: the line AutoImport logs after its loss check
// and backup and just before its replace import. It stands in,
// deterministically, for another process writing in that window.
type writeOnTrigger struct {
	slog.Handler
	write func()
}

// Handle runs the pending write on sync_import_triggered, then logs.
func (h *writeOnTrigger) Handle(ctx context.Context, r slog.Record) error {
	if r.Message == "sync_import_triggered" && h.write != nil {
		write := h.write
		h.write = nil
		write()
	}
	return h.Handler.Handle(ctx, r)
}

// TestAutoImport_WriteBetweenLossCheckAndReplace_WritesNothing verifies a
// write committed after the loss check and before the replace survives:
// the replace, which re-checks the store inside its transaction, imports
// nothing and does not record the file as imported, and the next command
// runs the guard again instead of applying the file.
func TestAutoImport_WriteBetweenLossCheckAndReplace_WritesNothing(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	hook := &writeOnTrigger{Handler: slog.NewTextHandler(io.Discard, nil)}
	f.svc = service.NewSyncService(f.store, slog.New(hook), func() time.Time { return f.now })
	f.svc.SetNoticeWriter(f.notices)
	hash := string(f.read(t, "data/sync.sha256"))
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))
	hook.write = func() { require.NoError(t, f.store.ClaimNode(ctx, "PROJ-2", "agent-concurrent")) }

	err := f.svc.AutoImport(ctx, f.mtixDir)
	require.Nil(t, hook.write, "the write ran between the loss check and the replace")
	require.ErrorIs(t, err, sqlite.ErrStoreChangedSinceCheck)
	assertClaimSurvivesNothingImported(t, f)
	assert.Equal(t, hash, string(f.read(t, "data/sync.sha256")), "the file is not recorded as imported")
	assert.NotContains(t, f.notices.String(), "mtix: imported")

	err = f.svc.AutoImport(ctx, f.mtixDir) // the next command runs the guard again
	require.ErrorIs(t, err, service.ErrAutoImportRefused)
	assertClaimSurvivesNothingImported(t, f)
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.Equal(t, "conflict", report.AutoImport.LastRefusal.Kind)
}

// assertClaimSurvivesNothingImported checks PROJ-2 still holds the
// concurrent claim and PROJ-3, from the file, was not imported.
func assertClaimSurvivesNothingImported(t *testing.T, f *guardFixture) {
	t.Helper()
	node, err := f.store.GetNode(context.Background(), "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, "agent-concurrent", node.Assignee, "the concurrent write survives")
	_, err = f.store.GetNode(context.Background(), "PROJ-3")
	assert.ErrorIs(t, err, model.ErrNotFound, "nothing from the file was imported")
}
