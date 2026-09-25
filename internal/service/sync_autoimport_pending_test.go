// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.2: while an auto-import of the tasks.json on disk is
// pending (refused, not resolved), auto-export leaves that file alone, so
// the next write does not silently drop the teammate's changes; the write
// is kept in the local store and one line says so. mtix sync --fix
// (ForceExport) and an mtix import of that file (ResolveRefusalByImport)
// resolve it. Written red-first.
package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// refusedFixture returns a project whose auto-import of a teammate's board
// (lacking PROJ-1's annotations) was refused, and that board.
func refusedFixture(t *testing.T) (*guardFixture, []byte) {
	t.Helper()
	f := newGuardFixture(t)
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { d.Nodes[nodeIndex(t, d, "PROJ-1")].Annotations = nil })
	f.pull(t, board)
	require.Error(t, f.svc.AutoImport(context.Background(), f.mtixDir))
	f.notices.Reset()
	return f, board
}

// writeLocalTask adds task id to the local store, as a writing command does.
func writeLocalTask(t *testing.T, f *guardFixture, id string, seq int) {
	t.Helper()
	now := time.Date(2026, 9, 24, 11, 0, 0, 0, time.UTC)
	require.NoError(t, f.store.CreateNode(context.Background(), &model.Node{
		ID: id, Project: "PROJ", Depth: 0, Seq: seq, Title: "Local task " + id,
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: now, UpdatedAt: now,
	}))
}

// TestAutoExport_WhileRefusalPending_LeavesTasksJSON verifies the write is
// kept in the store, tasks.json keeps the teammate's board, the line is
// printed once for repeated exports, and importing another file does not
// resolve the refusal while importing the refused one does.
func TestAutoExport_WhileRefusalPending_LeavesTasksJSON(t *testing.T) {
	ctx := context.Background()
	f, board := refusedFixture(t)
	writeLocalTask(t, f, "PROJ-4", 4)

	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	assert.Equal(t, string(board), string(f.read(t, "tasks.json")), "the teammate's board is not overwritten")
	assert.Equal(t, 1, strings.Count(f.notices.String(), ".mtix/tasks.json was not rewritten"), f.notices.String())
	assert.Contains(t, f.notices.String(), "mtix import .mtix/tasks.json --mode merge")
	_, err := f.store.GetNode(ctx, "PROJ-4")
	require.NoError(t, err, "the write is in the local store")

	other := filepath.Join(t.TempDir(), "other.json")
	require.NoError(t, os.WriteFile(other, []byte(`{"nodes":[]}`), 0o644))
	require.NoError(t, f.svc.ResolveRefusalByImport(f.mtixDir, other))
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	assert.Equal(t, string(board), string(f.read(t, "tasks.json")), "importing another file resolves nothing")

	require.NoError(t, f.svc.ResolveRefusalByImport(f.mtixDir, filepath.Join(f.mtixDir, "tasks.json")))
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	assert.Contains(t, string(f.read(t, "tasks.json")), `"PROJ-4"`, "the resolved board is rewritten")
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.NotEmpty(t, report.AutoImport.LastRefusal.ResolvedAt)
	assert.False(t, report.AutoImport.LastRefusal.Pending)
}

// TestAutoExport_PendingRefusalForAnotherFile_Exports verifies a refusal
// holds only for the file it refused: when a later board (a new pull) is on
// disk, the old refusal does not block it, and the export imports that
// board first and then writes the store.
func TestAutoExport_PendingRefusalForAnotherFile_Exports(t *testing.T) {
	ctx := context.Background()
	f, _ := refusedFixture(t)
	later := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
	f.pull(t, later)

	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	_, err := f.store.GetNode(ctx, "PROJ-3")
	require.NoError(t, err, "the later board is imported")
	assert.Contains(t, string(f.read(t, "tasks.json")), `"PROJ-3"`)
	assert.NotContains(t, f.notices.String(), "was not rewritten")
}

// TestAutoImport_RefusedFileImportedSince_NotPending verifies a refusal is
// no longer pending once the refused file is the stored, imported one (for
// example imported by another process that shares this store).
func TestAutoImport_RefusedFileImportedSince_NotPending(t *testing.T) {
	ctx := context.Background()
	f, board := refusedFixture(t)
	writeStoredHash(t, filepath.Dir(f.mtixDir), hashBytes(board))

	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.False(t, report.AutoImport.LastRefusal.Pending)
	writeLocalTask(t, f, "PROJ-4", 4)
	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	assert.Contains(t, string(f.read(t, "tasks.json")), `"PROJ-4"`, "no pending import blocks the export")
}

// TestForceExport_WhileRefusalPending_RewritesAndResolves verifies mtix sync
// --fix rewrites the refused tasks.json from the store and resolves the
// refusal; a failure to read the record never blocks it.
func TestForceExport_WhileRefusalPending_RewritesAndResolves(t *testing.T) {
	ctx := context.Background()
	f, board := refusedFixture(t)
	writeLocalTask(t, f, "PROJ-4", 4)

	require.NoError(t, f.svc.ForceExport(ctx, f.mtixDir))
	assert.NotEqual(t, string(board), string(f.read(t, "tasks.json")))
	assert.Contains(t, string(f.read(t, "tasks.json")), `"PROJ-4"`)
	assert.NotContains(t, f.notices.String(), "was not rewritten")
}

// TestAutoExport_BoardChangedOnDisk_NeverOverwritten is the round-3
// regression for writers that do not auto-import before they write (the
// MCP server, the daemon): a tasks.json that changed on disk since mtix last
// wrote or imported it is never overwritten. The export runs the automatic
// import first; if that imports the board, the export continues, otherwise
// the board is kept, the refusal is recorded as pending and one line says
// so.
func TestAutoExport_BoardChangedOnDisk_NeverOverwritten(t *testing.T) {
	tests := []struct {
		name       string
		switchOff  bool
		writeFirst bool
		wantImport bool
		wantKind   string
	}{
		{"no local write since the last export: the board is imported, then exported", false, false, true, ""},
		{"a local write since: a conflict, the board is kept", false, true, false, "conflict"},
		{"auto-import switched off: the board is kept", true, true, false, "not_imported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newGuardFixture(t)
			if tt.switchOff {
				f.svc.SetAutoSyncConfig(autoSyncSwitch(false))
			}
			board := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
			f.pull(t, board) // git pull while a long-running server holds the store
			if tt.writeFirst {
				writeLocalTask(t, f, "PROJ-4", 4)
			}

			require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
			_, err := f.store.GetNode(ctx, "PROJ-3")
			if tt.wantImport {
				require.NoError(t, err, "the pulled board is imported first")
				assert.Contains(t, f.notices.String(), "mtix: imported the changed .mtix/tasks.json")
				assert.NotContains(t, f.notices.String(), "was not rewritten")
				exported := f.read(t, "tasks.json")
				assert.Contains(t, string(exported), `"PROJ-3"`)
				assert.Equal(t, hashBytes(exported), string(f.read(t, "data/sync.sha256")), "then exported")
				report, compareErr := f.svc.Compare(ctx, f.mtixDir)
				require.NoError(t, compareErr)
				assert.Nil(t, report.AutoImport.LastRefusal, "a successful import records no refusal (MTIX-95.31.4)")
				return
			}
			assert.ErrorIs(t, err, model.ErrNotFound)
			assert.Equal(t, string(board), string(f.read(t, "tasks.json")), "the pulled board is not overwritten")
			assert.Contains(t, f.notices.String(), ".mtix/tasks.json was not rewritten")
			report, err := f.svc.Compare(ctx, f.mtixDir)
			require.NoError(t, err)
			require.NotNil(t, report.AutoImport.LastRefusal)
			assert.True(t, report.AutoImport.LastRefusal.Pending)
			assert.Equal(t, tt.wantKind, report.AutoImport.LastRefusal.Kind)
		})
	}
}

// TestAutoExport_NoStoredHashAutoSyncOff_KeepsBoardPending verifies the
// board is protected when no stored hash exists (a restored or copied
// database, MTIX-95.31.4): with sync.auto_sync false, a changed board and a
// local write, the write's export keeps the board and records a pending
// not_imported refusal.
func TestAutoExport_NoStoredHashAutoSyncOff_KeepsBoardPending(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.svc.SetAutoSyncConfig(autoSyncSwitch(false))
	require.NoError(t, os.Remove(filepath.Join(f.mtixDir, "data", "sync.sha256")))
	board := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
	f.pull(t, board)
	writeLocalTask(t, f, "PROJ-4", 4)

	require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
	assert.Equal(t, string(board), string(f.read(t, "tasks.json")), "the board is kept")
	report, err := f.svc.Compare(ctx, f.mtixDir)
	require.NoError(t, err)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.Equal(t, "not_imported", report.AutoImport.LastRefusal.Kind)
	assert.True(t, report.AutoImport.LastRefusal.Pending)
}

// TestForceExport_BoardChangedOnDisk_KeepsLocalStore verifies mtix sync --fix
// keeps the local store and rewrites a changed tasks.json from it, without
// importing the file first.
func TestForceExport_BoardChangedOnDisk_KeepsLocalStore(t *testing.T) {
	ctx := context.Background()
	f := newGuardFixture(t)
	f.pull(t, f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) }))

	require.NoError(t, f.svc.ForceExport(ctx, f.mtixDir))
	_, err := f.store.GetNode(ctx, "PROJ-3")
	assert.ErrorIs(t, err, model.ErrNotFound, "sync --fix does not import the file")
	assert.NotContains(t, string(f.read(t, "tasks.json")), `"PROJ-3"`, "the file is rewritten from the store")
}

// TestAutoExport_PendingNotice_SpecificToRefusalKind verifies the line an
// export prints while an import is pending names the way out for that kind
// of refusal: a newer schema needs an upgrade (never sync --fix, which would
// rewrite the board in the older format), a conflict lists its options, and
// the conflict warning no longer suggests mtix export.
func TestAutoExport_PendingNotice_SpecificToRefusalKind(t *testing.T) {
	tests := []struct {
		name    string
		board   func(t *testing.T, f *guardFixture) []byte
		write   bool
		want    []string
		notWant []string
	}{
		{"newer schema", func(t *testing.T, f *guardFixture) []byte {
			return f.teammateBoard(t, func(d *sqlite.ExportData) { d.SchemaVersion = "3.0.0" })
		}, false, []string{"upgrade mtix"}, []string{"mtix sync --fix", "--mode replace"}},
		{"conflict", func(t *testing.T, f *guardFixture) []byte {
			return f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
		}, true, []string{"Both it and the local store changed", "mtix import .mtix/tasks.json --mode merge",
			"--mode replace", "mtix sync --fix"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newGuardFixture(t)
			board := tt.board(t, f)
			if tt.write {
				writeLocalTask(t, f, "PROJ-4", 4)
			}
			f.pull(t, board)
			_ = f.svc.AutoImport(ctx, f.mtixDir) // the command's own auto-import: refused
			writeLocalTask(t, f, "PROJ-5", 5)
			require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))

			assert.Equal(t, string(board), string(f.read(t, "tasks.json")))
			notice := f.notices.String()
			for _, w := range tt.want {
				assert.Contains(t, notice, w)
			}
			for _, w := range tt.notWant {
				assert.NotContains(t, notice, w)
			}
			assert.NotContains(t, f.logs.String(), "'mtix export'", "the conflict warning names commands that resolve it")
		})
	}
}

// TestAutoImport_InvalidBoard_RecordedPendingAndKept verifies a board that
// fails a check (a hand-merged edit that left the checksum stale, git
// conflict markers, a file that is not an export) is recorded as a pending
// refusal naming the review path, and the next write does not overwrite
// it.
func TestAutoImport_InvalidBoard_RecordedPendingAndKept(t *testing.T) {
	tests := []struct {
		name   string
		board  func(sealed string) string
		reason string
	}{
		{"stale checksum", func(sealed string) string {
			return strings.Replace(sealed, "Teammate task PROJ-3", "Merged by hand", 1)
		}, "checksum"},
		{"git conflict markers", func(sealed string) string {
			return "<<<<<<< HEAD\n" + sealed + "=======\n" + sealed + ">>>>>>> theirs\n"
		}, "parse tasks.json"},
		{"not an export", func(string) string { return "[]\n" }, "not an mtix export"},
		{"truncated JSON", func(sealed string) string { return sealed[:len(sealed)/2] }, "parse tasks.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			f := newGuardFixture(t)
			sealed := f.teammateBoard(t, func(d *sqlite.ExportData) { addTeammateNode(t, d, "PROJ-2", "PROJ-3", 3) })
			board := []byte(tt.board(string(sealed)))
			f.pull(t, board)

			_ = f.svc.AutoImport(ctx, f.mtixDir)
			report, err := f.svc.Compare(ctx, f.mtixDir)
			if err == nil {
				require.NotNil(t, report.AutoImport.LastRefusal)
				assert.True(t, report.AutoImport.LastRefusal.Pending)
				assert.Equal(t, "invalid_file", report.AutoImport.LastRefusal.Kind)
				assert.Contains(t, report.AutoImport.LastRefusal.Reason, tt.reason)
			}

			writeLocalTask(t, f, "PROJ-4", 4)
			require.NoError(t, f.svc.AutoExport(ctx, f.mtixDir))
			assert.Equal(t, string(board), string(f.read(t, "tasks.json")), "the board is kept")
			assert.Contains(t, f.notices.String(), "mtix import .mtix/tasks.json --recompute-checksum")
			assert.Contains(t, f.notices.String(), "mtix sync --fix")
		})
	}
}
