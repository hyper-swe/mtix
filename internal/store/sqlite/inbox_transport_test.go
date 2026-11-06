// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Export/import must move inbox identities, never local sequence watermarks.
func transportExport(t *testing.T, s *sqlite.Store) *sqlite.ExportData {
	t.Helper()
	data, err := s.ExportWithInbox(t.Context(), "", "")
	require.NoError(t, err)
	return data
}

func TestImport_FreshJournalRoundTrip_PreservesCoverage(t *testing.T) {
	for _, mode := range []sqlite.ImportMode{sqlite.ImportModeMerge, sqlite.ImportModeReplace} {
		t.Run(string(mode), func(t *testing.T) {
			source := newTestStore(t)
			first, _, last := addressThree(t, source, "recipient")
			require.NoError(t, source.InboxAckThrough(t.Context(), "recipient", first))
			require.NoError(t, source.InboxAck(t.Context(), "recipient", last))
			before, err := source.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			file := transportExport(t, source)
			destination := newTestStore(t)
			_, err = destination.Import(t.Context(), file, mode, false)
			require.NoError(t, err)
			got, err := destination.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			require.Len(t, got, 1, "unacked middle event survives the move")
			require.Equal(t, before[0].EventID, got[0].EventID)
			require.NoError(t, destination.InboxAck(t.Context(), "recipient", got[0].Seq))
			_, err = destination.Import(t.Context(), file, mode, false)
			require.NoError(t, err)
			got, err = destination.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			require.Empty(t, got, "stale import cannot undo a destination acknowledgment")
		})
	}
}

func TestExport_OrdinarySnapshot_OmitsJournal(t *testing.T) {
	s := newTestStore(t)
	addressThree(t, s, "recipient")
	board, err := s.Export(t.Context(), "", "")
	require.NoError(t, err)
	raw, err := json.Marshal(board)
	require.NoError(t, err)
	require.NotContains(t, string(raw), "inbox_journal")
	explicit := transportExport(t, s)
	raw, err = json.Marshal(explicit)
	require.NoError(t, err)
	require.Contains(t, string(raw), "inbox_journal")
	require.Equal(t, board.Checksum, explicit.Checksum)
}

func TestRecover_ReadableDatabase_PreservesMail(t *testing.T) {
	s, raw := retentionStore(t)
	addressThree(t, s, "recipient")
	var dbPath string
	require.NoError(t, raw.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&dbPath))
	result, err := sqlite.Recover(t.Context(), dbPath, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	destination := newTestStore(t)
	_, err = destination.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := destination.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 3)
}

func TestImport_ConflictingIdentity_RollsBackAllWrites(t *testing.T) {
	for _, mode := range []sqlite.ImportMode{sqlite.ImportModeMerge, sqlite.ImportModeReplace} {
		t.Run(string(mode), func(t *testing.T) {
			source := newTestStore(t)
			addressThree(t, source, "recipient")
			file := transportExport(t, source)
			dest, raw := retentionStore(t)
			_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
			require.NoError(t, err)
			id := file.InboxJournal.Events[0].EventID
			_, err = raw.Exec(`UPDATE sync_events SET payload=? WHERE event_id=?`, `{"to":"recipient","body":"different"}`, id)
			require.NoError(t, err)
			before, err := dest.Export(t.Context(), "", "")
			require.NoError(t, err)
			_, err = dest.Import(t.Context(), file, mode, false)
			require.ErrorIs(t, err, model.ErrConflict)
			after, err := dest.Export(t.Context(), "", "")
			require.NoError(t, err)
			require.Equal(t, before.Checksum, after.Checksum)
			var payload string
			require.NoError(t, raw.QueryRow(`SELECT payload FROM sync_events WHERE event_id=?`, id).Scan(&payload))
			require.Contains(t, payload, "different")
		})
	}
}

func TestImport_UnrelatedDestinationWork_RemapDoesNotHideOrSuppressIt(t *testing.T) {
	source := newTestStore(t)
	first, _, last := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAckThrough(t.Context(), "recipient", first))
	require.NoError(t, source.InboxAck(t.Context(), "recipient", last))
	file := transportExport(t, source)
	dest, raw := retentionStore(t)
	// An unrelated event occupies a source sequence, without a node conflict.
	_, err := raw.Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,
 wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
 VALUES('local-event','TEST','TEST-9','comment','{"to":"recipient","body":"local"}',0,1,'{}','sender','','2026-11-05T00:00:00Z')`)
	require.NoError(t, err)
	_, err = dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "local-event", got[0].EventID)
	hooks, err := dest.ReadJournalSince(t.Context(), 0, 100)
	require.NoError(t, err)
	require.Len(t, hooks, 1)
	require.Equal(t, "local-event", hooks[0].EventID)
	pending, err := dest.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, "local-event", pending[0].EventID)
}

func TestExportWithInbox_Released054Reader_ImportsNonemptyJournal(t *testing.T) {
	reader := compat054Binary(t)
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	file := transportExport(t, source)
	raw, err := json.Marshal(file)
	require.NoError(t, err)
	project := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(project, ".mtix", "data"), 0700))
	require.NoError(t, os.WriteFile(filepath.Join(project, ".mtix", "config.yaml"), []byte("prefix: PROJ\nsync:\n  auto_sync: false\n"), 0600))
	incoming := filepath.Join(t.TempDir(), "private.json")
	require.NoError(t, os.WriteFile(incoming, raw, 0600))
	attachReaderCommand(t, reader, project, "", "import", incoming)
	attachReaderCommand(t, reader, project, "", "show", "PROJ-1", "--json")
}

func TestRecover_ExplicitExport_PreservesMailAndCoverage(t *testing.T) {
	source := newTestStore(t)
	first, _, _ := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAckThrough(t.Context(), "recipient", first))
	file := transportExport(t, source)
	raw, err := json.Marshal(file)
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "private.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	result, err := sqlite.Recover(t.Context(), filepath.Join(dir, "absent.db"), path, "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestRecover_UnreadableAcks_KeepsReadableMail(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	var path string
	require.NoError(t, raw.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path))
	_, err := raw.Exec(`DROP TABLE agent_inbox_ack`)
	require.NoError(t, err)
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 3, "unreadable acks must not discard readable mail")
	require.Contains(t, strings.Join(result.Notes, " "), "acknowledgment")
}

func TestImportReconcile_JournalOnlyChange_CreatesBackup(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	board, err := source.Export(t.Context(), "", "")
	require.NoError(t, err)
	file := transportExport(t, source)
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), board, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	backups := 0
	options := sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, BeforeWrite: func() error { backups++; return nil }}
	_, _, err = dest.ImportReconcile(t.Context(), file, options)
	require.NoError(t, err)
	require.Equal(t, 1, backups, "journal-only changes require the same pre-import backup")
	_, _, err = dest.ImportReconcile(t.Context(), file, options)
	require.NoError(t, err)
	require.Equal(t, 1, backups, "idempotent second import needs no backup")
}

func TestImport_InvalidJournalEnvelope_LeavesDestinationUntouched(t *testing.T) {
	for _, mode := range []sqlite.ImportMode{sqlite.ImportModeMerge, sqlite.ImportModeReplace} {
		for _, kind := range []string{"version", "checksum", "payload"} {
			t.Run(string(mode)+"/"+kind, func(t *testing.T) {
				source := newTestStore(t)
				addressThree(t, source, "recipient")
				file := transportExport(t, source)
				switch kind {
				case "version":
					file.InboxJournal.Version++
				case "checksum":
					file.InboxJournal.Checksum = "bad"
				case "payload":
					file.InboxJournal.Events[0].Payload = `{"to":"recipient","body":"tampered"}`
				}
				dest := newTestStore(t)
				before, err := dest.ExportWithInbox(t.Context(), "", "")
				require.NoError(t, err)
				_, err = dest.Import(t.Context(), file, mode, false)
				require.ErrorIs(t, err, model.ErrInvalidInput)
				after, err := dest.ExportWithInbox(t.Context(), "", "")
				require.NoError(t, err)
				require.Equal(t, before.Checksum, after.Checksum)
				require.Equal(t, before.InboxJournal, after.InboxJournal)
			})
		}
	}
}

func TestImport_RestoreStorageFailure_RollsBackBoardAndJournal(t *testing.T) {
	for _, table := range []string{"sync_events", "hook_suppressed_events", "inbox_restored_events", "agent_inbox_ack"} {
		t.Run(table, func(t *testing.T) {
			source := newTestStore(t)
			first, _, _ := addressThree(t, source, "recipient")
			require.NoError(t, source.InboxAck(t.Context(), "recipient", first))
			file := transportExport(t, source)
			dest, raw := retentionStore(t)
			before, err := dest.Export(t.Context(), "", "")
			require.NoError(t, err)
			// The controlled table name is one of the four schema names above.
			_, err = raw.Exec(`CREATE TRIGGER reject_restore BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(ABORT,'injected restore failure'); END`)
			require.NoError(t, err)
			_, err = dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
			require.ErrorContains(t, err, "injected restore failure")
			after, err := dest.Export(t.Context(), "", "")
			require.NoError(t, err)
			require.Equal(t, before.Checksum, after.Checksum)
			var count int
			require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_events`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestRecover_DamagedTimestamp_ReportsKnownLoss(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	_, err := raw.Exec(`UPDATE sync_events SET created_at='broken' WHERE rowid=(SELECT MIN(rowid) FROM sync_events WHERE op_type='comment')`)
	require.NoError(t, err)
	exported, err := source.ExportWithInbox(t.Context(), "", "")
	require.NoError(t, err)
	require.Len(t, exported.InboxJournal.Events, 2)
	require.Contains(t, strings.Join(exported.InboxJournal.Notes, " "), "timestamp")
	var path string
	require.NoError(t, raw.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path))
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.Len(t, result.Export.InboxJournal.Events, 2)
	require.Contains(t, strings.Join(result.Notes, " "), "1 addressed inbox event(s) unreadable and omitted")
	require.NoError(t, sqlite.ValidateExport(result.Export))
}
