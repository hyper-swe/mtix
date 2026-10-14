// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// historyFile returns a 2.1.0 export of the golden board with one orphan entry
// and one entry on the root task, round-tripped through JSON as a file read.
func historyFile(t *testing.T) (*historyFixture, *sqlite.ExportData) {
	t.Helper()
	f, raw := goldenStore(t)
	insertHistory(t, f.db, "01H0000000000000000000F001", goldenRootUID(t, raw), "status")
	insertHistory(t, f.db, "01H0000000000000000000F002", "ghost-uid", "status")
	f.s.SetHistoryExportSource(func() bool { return true })
	exp, err := f.s.Export(context.Background(), "PROJ", "v")
	require.NoError(t, err)
	var file sqlite.ExportData
	require.NoError(t, json.Unmarshal(marshalExport(t, exp), &file))
	return f, &file
}

// TestImport_HistoryEntryValidation_RejectsUnstorableEntries: every entry's
// own fields, and its place in the file, are checked before anything is written.
func TestImport_HistoryEntryValidation_RejectsUnstorableEntries(t *testing.T) {
	cases := []struct {
		name string
		edit func(f *sqlite.ExportData)
	}{
		{"orphan names a live task", func(f *sqlite.ExportData) { f.History[0].TaskUID = f.Nodes[0].UID }},
		{"orphan unknown kind", func(f *sqlite.ExportData) { f.History[0].Kind = "bogus-kind" }},
		{"orphan unknown origin", func(f *sqlite.ExportData) { f.History[0].Origin = "bogus-origin" }},
		{"orphan ts not a time", func(f *sqlite.ExportData) { f.History[0].TS = "yesterday" }},
		{"orphan ts not fixed width", func(f *sqlite.ExportData) { f.History[0].TS = "2026-03-14T12:00:00Z" }},
		{"orphan no actor", func(f *sqlite.ExportData) { f.History[0].Actor = "" }},
		{"orphan no field", func(f *sqlite.ExportData) { f.History[0].Field = "" }},
		{"orphan no task_uid", func(f *sqlite.ExportData) { f.History[0].TaskUID = "" }},
		{"node entry unknown kind", func(f *sqlite.ExportData) { f.Nodes[0].History[0].Kind = "bogus-kind" }},
		{"node entry unknown origin", func(f *sqlite.ExportData) { f.Nodes[0].History[0].Origin = "bogus" }},
		{"node entry ts not a time", func(f *sqlite.ExportData) { f.Nodes[0].History[0].TS = "yesterday" }},
		{"node entry names another task", func(f *sqlite.ExportData) { f.Nodes[0].History[0].TaskUID = "other-uid" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, file := historyFile(t)
			require.Len(t, file.History, 1)
			require.Len(t, file.Nodes[0].History, 1)
			tc.edit(file)
			require.NoError(t, sqlite.RecomputeExportChecksum(file))
			dst := newHistoryFixture(t)
			_, err := dst.s.Import(context.Background(), file, sqlite.ImportModeReplace, true)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			var n int
			require.NoError(t, dst.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE entry_id LIKE '01H0000000000000000000F%'`).Scan(&n))
			require.Equal(t, 0, n, "a refused file writes nothing")
		})
	}
}

// TestExportHistory_EventIDRoundTripsAndEventKeyDedupes: the causal event id
// survives export and replace import, and two entries of one (task_uid,
// event_id, field) key store once.
func TestExportHistory_EventIDRoundTripsAndEventKeyDedupes(t *testing.T) {
	src, raw := goldenStore(t)
	uid := goldenRootUID(t, raw)
	_, err := src.db.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field, old_value, new_value, event_id)
		VALUES ('01H0000000000000000000G001', ?, '2026-03-14T12:00:00.000000000Z', 'a', 'hub', 'change', 'status', NULL, NULL, 'evt-1')`, uid)
	require.NoError(t, err)
	src.s.SetHistoryExportSource(func() bool { return true })
	exp, err := src.s.Export(context.Background(), "PROJ", "v")
	require.NoError(t, err)
	require.Equal(t, "evt-1", exp.Nodes[0].History[0].EventID)
	require.Contains(t, string(marshalExport(t, exp)), `"event_id": "evt-1"`)

	dst := newHistoryFixture(t)
	_, err = dst.s.Import(context.Background(), exp, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
	var ev string
	require.NoError(t, dst.db.QueryRow(`SELECT event_id FROM task_history WHERE entry_id = '01H0000000000000000000G001'`).Scan(&ev))
	require.Equal(t, "evt-1", ev)

	dup := exp.Nodes[0].History[0]
	dup.ID = "01H0000000000000000000G002"
	exp.Nodes[0].History = append(exp.Nodes[0].History, dup)
	require.NoError(t, sqlite.RecomputeExportChecksum(exp))
	other := newHistoryFixture(t)
	_, err = other.s.Import(context.Background(), exp, sqlite.ImportModeReplace, true)
	require.NoError(t, err)
	var n int
	require.NoError(t, other.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE event_id = 'evt-1'`).Scan(&n))
	require.Equal(t, 1, n)
}

// TestExportHistory_OrphanOrderIsTaskTimeID: same-task, same-time orphans
// written out of order are exported by entry id, and a recomputed checksum
// agrees with the exporter's.
func TestExportHistory_OrphanOrderIsTaskTimeID(t *testing.T) {
	f, _ := goldenStore(t)
	for _, id := range []string{"01H0000000000000000000H003", "01H0000000000000000000H001", "01H0000000000000000000H002"} {
		insertHistory(t, f.db, id, "ghost-uid", "status")
	}
	f.s.SetHistoryExportSource(func() bool { return true })
	exp, err := f.s.Export(context.Background(), "PROJ", "v")
	require.NoError(t, err)
	require.Equal(t, []string{"01H0000000000000000000H001", "01H0000000000000000000H002", "01H0000000000000000000H003"},
		[]string{exp.History[0].ID, exp.History[1].ID, exp.History[2].ID})

	written := exp.Checksum
	exp.History[0], exp.History[2] = exp.History[2], exp.History[0] // a reader's shuffled copy
	require.NoError(t, sqlite.RecomputeExportChecksum(exp))
	require.Equal(t, written, exp.Checksum, "recompute sorts the list as the exporter does")
	ok, err := sqlite.VerifyExportChecksum(exp)
	require.NoError(t, err)
	require.True(t, ok)
}

// TestImportMerge_HistoryNotTakenYet pins the documented limit: a merge
// import does not take a file's history (the union is MTIX-95.31.19.9).
func TestImportMerge_HistoryNotTakenYet(t *testing.T) {
	_, file := historyFile(t)
	dst := newHistoryFixture(t)
	_, err := dst.s.Import(context.Background(), file, sqlite.ImportModeMerge, true)
	require.NoError(t, err)
	var n int
	require.NoError(t, dst.db.QueryRow(`SELECT COUNT(*) FROM task_history WHERE entry_id LIKE '01H0000000000000000000F%'`).Scan(&n))
	require.Equal(t, 0, n)
}
