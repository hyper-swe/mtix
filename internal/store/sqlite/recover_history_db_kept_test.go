// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Entry ids the database alone holds in the recover tests below: one on the
// golden root task, one on a task that no longer exists (an orphan).
const (
	recoverDBLiveEntry   = "01H0000000000000000000W001"
	recoverDBOrphanEntry = "01H0000000000000000000W002"
	// recoverMirrorEntry is held by the 2.1.0 mirror only.
	recoverMirrorEntry = "01H0000000000000000000W003"
)

// recoveredEntryIDs lists every entry id in a recovered export, node histories
// and the orphan list, sorted, with repeats kept so a duplicate shows.
func recoveredEntryIDs(d *sqlite.ExportData) []string {
	var ids []string
	for _, n := range d.Nodes {
		for _, e := range n.History {
			ids = append(ids, e.ID)
		}
	}
	for _, e := range d.History {
		ids = append(ids, e.ID)
	}
	slices.Sort(ids)
	return ids
}

// storedRecoveredIDs lists the entry ids of the rows the recovery's entries
// became in a store: origin local. The import's own entries (origin import)
// are not recovered history.
func storedRecoveredIDs(t *testing.T, db *sql.DB) []string {
	t.Helper()
	rows, err := db.Query(`SELECT entry_id FROM task_history WHERE origin = 'local' ORDER BY entry_id`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
	}
	require.NoError(t, rows.Err())
	return ids
}

// recoverMirrorCase is one mirror state; mirrorOnly lists the entry ids that
// only the mirror holds, which the recovered file must add to the database's.
type recoverMirrorCase struct {
	name       string
	mirror     func(t *testing.T, f *historyFixture, raw []byte) string
	mirrorOnly []string
}

// recoverMirrorCases are every mirror state recovery can meet. The database's
// history must survive each one: the switch only governs what tasks.json
// carries, and the recovered file is private (MTIX-95.31.19.5.3).
func recoverMirrorCases() []recoverMirrorCase {
	return []recoverMirrorCase{
		{name: "plain 2.0.0 mirror, the default with history.export off", mirror: func(t *testing.T, _ *historyFixture, raw []byte) string {
			path := filepath.Join(t.TempDir(), "tasks.json")
			require.NoError(t, os.WriteFile(path, raw, 0o600))
			return path
		}},
		{name: "no mirror", mirror: func(t *testing.T, _ *historyFixture, _ []byte) string {
			return filepath.Join(t.TempDir(), "absent-tasks.json")
		}},
		{name: "unreadable mirror", mirror: func(t *testing.T, _ *historyFixture, _ []byte) string {
			path := filepath.Join(t.TempDir(), "tasks.json")
			require.NoError(t, os.WriteFile(path, []byte("{not json"), 0o600))
			return path
		}},
		{name: "2.1.0 mirror with history", mirrorOnly: []string{recoverMirrorEntry}, mirror: func(t *testing.T, f *historyFixture, raw []byte) string {
			path := mirrorWithHistory(t, f, goldenRootUID(t, raw), recoverMirrorEntry)
			f.s.SetHistoryExportSource(func() bool { return false })
			// The mirror alone holds this entry: the database must not.
			_, err := f.db.Exec(`DELETE FROM task_history WHERE entry_id = ?`, recoverMirrorEntry)
			require.NoError(t, err)
			return path
		}},
	}
}

// TestRecover_DatabaseHistory_KeptWhateverTheMirror: every task_history entry
// the damaged database still yields reaches the recovered file, on its task or
// in the orphan list, labeled 2.1.0, whatever the mirror's format, and
// survives the replace import into a fresh store (MTIX-95.31.19.5.3).
func TestRecover_DatabaseHistory_KeptWhateverTheMirror(t *testing.T) {
	for _, tc := range recoverMirrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, raw := goldenStore(t)
			mirrorPath := tc.mirror(t, f, raw)
			insertHistory(t, f.db, recoverDBLiveEntry, goldenRootUID(t, raw), "status")
			insertHistory(t, f.db, recoverDBOrphanEntry, "ghost-uid", "priority")

			res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", sqlite.RecoverOptions{Logger: slog.Default()})
			require.NoError(t, err)
			require.Equal(t, sqlite.SchemaVersionHistory, res.Export.SchemaVersion)
			// The exact set: the database's two entries plus the mirror-only
			// ones; nothing invented, repeated or lost.
			want := append([]string{recoverDBLiveEntry, recoverDBOrphanEntry}, tc.mirrorOnly...)
			slices.Sort(want)
			require.Equal(t, want, recoveredEntryIDs(res.Export), "the recovered export holds exactly these entries")
			onNodes := map[string]bool{}
			for _, n := range res.Export.Nodes {
				for _, e := range n.History {
					onNodes[e.ID] = true
				}
			}
			require.True(t, onNodes[recoverDBLiveEntry], "the live task's entry is on its node")
			var orphans []string
			for _, e := range res.Export.History {
				orphans = append(orphans, e.ID)
			}
			require.Equal(t, []string{recoverDBOrphanEntry}, orphans, "only the entry of the missing task is an orphan")
			require.Contains(t, strings.Join(res.Notes, "\n"), "task history entries were salvaged")
			ok, err := sqlite.VerifyExportChecksum(res.Export)
			require.NoError(t, err)
			require.True(t, ok)

			dst := newHistoryFixture(t)
			_, err = dst.s.Import(context.Background(), res.Export, sqlite.ImportModeReplace, true)
			require.NoError(t, err)
			require.Equal(t, want, storedRecoveredIDs(t, dst.db), "the store holds exactly the recovered entries after the import")
		})
	}
}

// TestRecover_UnreadableHistoryTable_ReportedWhateverTheMirror: a history
// table the database cannot read is a note with every mirror state, never
// silence (MTIX-95.31.19.5.3).
func TestRecover_UnreadableHistoryTable_ReportedWhateverTheMirror(t *testing.T) {
	for _, tc := range recoverMirrorCases() {
		t.Run(tc.name, func(t *testing.T) {
			f, raw := goldenStore(t)
			mirrorPath := tc.mirror(t, f, raw)
			_, err := f.db.Exec(`DROP TABLE task_history`)
			require.NoError(t, err)
			res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", sqlite.RecoverOptions{Logger: slog.Default()})
			require.NoError(t, err)
			require.Contains(t, strings.Join(res.Notes, "\n"), "task history unreadable")
		})
	}
}

// TestRecover_PartlyReadableHistoryTable_KeepsReadableEntriesWithANote: a
// history table with a row the reader cannot scan recovers the readable
// entries and says how many rows were skipped, with a 2.0.0 mirror and with no
// mirror (MTIX-95.31.19.5.3).
func TestRecover_PartlyReadableHistoryTable_KeepsReadableEntriesWithANote(t *testing.T) {
	for _, tc := range recoverMirrorCases()[:2] {
		t.Run(tc.name, func(t *testing.T) {
			f, raw := goldenStore(t)
			mirrorPath := tc.mirror(t, f, raw)
			// The same table without its NOT NULL constraints, so a row can
			// hold a NULL the scanner rejects.
			_, err := f.db.Exec(`DROP TABLE task_history`)
			require.NoError(t, err)
			_, err = f.db.Exec(`CREATE TABLE task_history (entry_id TEXT PRIMARY KEY, task_uid TEXT, ts TEXT, actor TEXT,
				origin TEXT, kind TEXT, field TEXT, old_value TEXT, new_value TEXT, event_id TEXT)`)
			require.NoError(t, err)
			insertHistory(t, f.db, recoverDBLiveEntry, goldenRootUID(t, raw), "status")
			insertHistory(t, f.db, recoverDBOrphanEntry, "ghost-uid", "priority")
			_, err = f.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field)
				VALUES ('01H0000000000000000000W009', ?, '2026-03-14T12:00:00.000000000Z', NULL, 'local', 'change', 'status')`, goldenRootUID(t, raw))
			require.NoError(t, err)

			res, err := sqlite.Recover(context.Background(), f.path, mirrorPath, "rv", sqlite.RecoverOptions{Logger: slog.Default()})
			require.NoError(t, err)
			notes := strings.Join(res.Notes, "\n")
			require.Contains(t, notes, "1 task history rows were unreadable and skipped")
			require.Contains(t, notes, "2 task history entries were salvaged")
			require.Equal(t, []string{recoverDBLiveEntry, recoverDBOrphanEntry}, recoveredEntryIDs(res.Export),
				"the readable entries are kept, the unscannable row is not invented")
			ok, err := sqlite.VerifyExportChecksum(res.Export)
			require.NoError(t, err)
			require.True(t, ok)
		})
	}
}
