// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestRecover_HistoryMirror_LabelsFormat210AndKeepsOrphans: a recovered board
// whose mirror carries history (format 2.1.0) is written as 2.1.0 with the
// per-node entries, the orphan list and the entries the database still holds,
// and it verifies and imports.
func TestRecover_HistoryMirror_LabelsFormat210AndKeepsOrphans(t *testing.T) {
	f, raw := goldenStore(t)
	insertHistory(t, f.db, "01H0000000000000000000R001", goldenRootUID(t, raw), "status")
	insertHistory(t, f.db, "01H0000000000000000000R002", "ghost-uid", "status")
	f.s.SetHistoryExportSource(func() bool { return true })
	mirror, err := f.s.Export(context.Background(), "PROJ", "v")
	require.NoError(t, err)
	mirrorPath := filepath.Join(t.TempDir(), "tasks.json")
	require.NoError(t, os.WriteFile(mirrorPath, marshalExport(t, mirror), 0o600))
	// An entry only the database holds.
	insertHistory(t, f.db, "01H0000000000000000000R003", goldenRootUID(t, raw), "priority")

	res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", slog.Default())
	require.NoError(t, err)
	require.Equal(t, sqlite.SchemaVersionHistory, res.Export.SchemaVersion)
	require.Len(t, res.Export.History, 1, "the orphan list is kept")
	got := 0
	for _, n := range res.Export.Nodes {
		got += len(n.History)
	}
	require.Equal(t, 2, got, "mirror entry plus the entry only the database holds")
	ok, err := sqlite.VerifyExportChecksum(res.Export)
	require.NoError(t, err)
	require.True(t, ok)
}

// TestRecover_PlainMirror_StaysFormat200: a mirror without history recovers
// as before, 2.0.0 and no history, even when the database holds entries.
func TestRecover_PlainMirror_StaysFormat200(t *testing.T) {
	f, raw := goldenStore(t)
	insertHistory(t, f.db, "01H0000000000000000000R004", goldenRootUID(t, raw), "status")
	mirrorPath := filepath.Join(t.TempDir(), "tasks.json")
	require.NoError(t, os.WriteFile(mirrorPath, raw, 0o600))
	res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", slog.Default())
	require.NoError(t, err)
	require.Equal(t, sqlite.SchemaVersionV1, res.Export.SchemaVersion)
	require.Empty(t, res.Export.History)
	for _, n := range res.Export.Nodes {
		require.Empty(t, n.History)
	}
}

// TestRecover_MalformedHistoryEntries_DroppedWithNotesFileStillImports: one
// malformed row in the database and one in the mirror are dropped and
// reported; the valid entries survive and the recovered file imports.
func TestRecover_MalformedHistoryEntries_DroppedWithNotesFileStillImports(t *testing.T) {
	f, raw := goldenStore(t)
	uid := goldenRootUID(t, raw)
	insertHistory(t, f.db, "01H0000000000000000000S001", uid, "status")
	insertHistory(t, f.db, "01H0000000000000000000S002", "ghost-uid", "status")
	f.s.SetHistoryExportSource(func() bool { return true })
	mirror, err := f.s.Export(context.Background(), "PROJ", "v")
	require.NoError(t, err)
	mirror.History[0].ID = "01H0000000000000000000S005" // a malformed orphan only the mirror holds
	mirror.History[0].Origin = "bogus-origin"
	require.NoError(t, sqlite.RecomputeExportChecksum(mirror))
	mirrorPath := filepath.Join(t.TempDir(), "tasks.json")
	require.NoError(t, os.WriteFile(mirrorPath, marshalExport(t, mirror), 0o600))
	insertHistory(t, f.db, "01H0000000000000000000S003", uid, "priority") // valid, database only
	_, err = f.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field)
		VALUES ('01H0000000000000000000S004', ?, '2026-03-14 12:00:00', 'tester', 'local', 'change', 'status')`, uid)
	require.NoError(t, err)

	res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", slog.Default())
	require.NoError(t, err)
	notes := strings.Join(res.Notes, "\n")
	require.Contains(t, notes, "2 task history entries were dropped")
	require.Contains(t, notes, "ts")
	require.Contains(t, notes, "origin")
	got := 0
	for _, n := range res.Export.Nodes {
		got += len(n.History)
	}
	require.Equal(t, 2, got, "S001 and S003 survive")
	require.Len(t, res.Export.History, 1, "S002, valid in the database, stays an orphan")
	dst := newHistoryFixture(t)
	_, err = dst.s.Import(context.Background(), res.Export, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
}

// mirrorWithHistory exports the golden board with the switch on, after adding
// entries with the given ids on the root task, and writes it as a mirror.
func mirrorWithHistory(t *testing.T, f *historyFixture, uid string, ids ...string) string {
	t.Helper()
	for _, id := range ids {
		insertHistory(t, f.db, id, uid, "status")
	}
	f.s.SetHistoryExportSource(func() bool { return true })
	mirror, err := f.s.Export(context.Background(), "PROJ", "v")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "tasks.json")
	require.NoError(t, os.WriteFile(path, marshalExport(t, mirror), 0o600))
	return path
}

// TestRecover_MalformedDatabaseCopy_FallsBackToTheMirrorsValidCopy: when the
// database holds a malformed copy of an entry the mirror holds intact, the
// mirror's copy is kept.
func TestRecover_MalformedDatabaseCopy_FallsBackToTheMirrorsValidCopy(t *testing.T) {
	f, raw := goldenStore(t)
	uid := goldenRootUID(t, raw)
	mirrorPath := mirrorWithHistory(t, f, uid, "01H0000000000000000000T001")
	_, err := f.db.Exec(`UPDATE task_history SET ts = '2026-03-14 12:00:00' WHERE entry_id = '01H0000000000000000000T001'`)
	require.NoError(t, err)

	res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", slog.Default())
	require.NoError(t, err)
	got := 0
	for _, n := range res.Export.Nodes {
		got += len(n.History)
	}
	require.Equal(t, 1, got, "the mirror's intact copy survives")
	dst := newHistoryFixture(t)
	_, err = dst.s.Import(context.Background(), res.Export, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
}

// TestRecover_OrphanOnAPlaceholderParentsID_MovesOntoThePlaceholder: history
// of a lost task that has no uid, whose id a placeholder parent takes, is
// attached to the placeholder, so the file still imports.
func TestRecover_OrphanOnAPlaceholderParentsID_MovesOntoThePlaceholder(t *testing.T) {
	f, raw := goldenStore(t)
	mirrorPath := mirrorWithHistory(t, f, goldenRootUID(t, raw), "01H0000000000000000000U001")
	var mirror sqlite.ExportData
	data, err := os.ReadFile(mirrorPath)
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(data, &mirror))
	// Drop the root; keep its child, so recovery synthesizes a placeholder for PROJ-1.
	mirror.Nodes = mirror.Nodes[1:]
	mirror.NodeCount = 1
	entry := `[{"id":"01H0000000000000000000U002","task_uid":"PROJ-1","ts":"2026-03-14T12:00:00.000000000Z","actor":"a","origin":"local","kind":"change","field":"status"}]`
	require.NoError(t, json.Unmarshal([]byte(entry), &mirror.History))
	require.NoError(t, sqlite.RecomputeExportChecksum(&mirror))
	require.NoError(t, os.WriteFile(mirrorPath, marshalExport(t, &mirror), 0o600))

	res, err := sqlite.Recover(context.Background(), filepath.Join(t.TempDir(), "absent.db"), mirrorPath, "rv", slog.Default())
	require.NoError(t, err)
	require.Contains(t, res.Placeholders, "PROJ-1")
	require.Empty(t, res.Export.History, "no orphan names a task the file holds")
	var onPlaceholder int
	for _, n := range res.Export.Nodes {
		if n.ID == "PROJ-1" {
			onPlaceholder = len(n.History)
		}
	}
	require.Equal(t, 1, onPlaceholder)
	dst := newHistoryFixture(t)
	_, err = dst.s.Import(context.Background(), res.Export, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
}

// TestRecover_UnreadableHistoryTable_IsReportedInTheNotes: a history table the
// database cannot read is a recovery note, not silence.
func TestRecover_UnreadableHistoryTable_IsReportedInTheNotes(t *testing.T) {
	f, raw := goldenStore(t)
	mirrorPath := mirrorWithHistory(t, f, goldenRootUID(t, raw), "01H0000000000000000000V001")
	_, err := f.db.Exec(`DROP TABLE task_history`)
	require.NoError(t, err)
	res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", slog.Default())
	require.NoError(t, err)
	require.Contains(t, strings.Join(res.Notes, "\n"), "task history unreadable")
	require.Equal(t, sqlite.SchemaVersionHistory, res.Export.SchemaVersion, "the mirror's entries are still carried")
}
