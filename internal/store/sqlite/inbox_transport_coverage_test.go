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

func TestExportWithInbox_MultipleRecipients_CoverageStaysWithRecipient(t *testing.T) {
	for _, recoverDB := range []bool{false, true} {
		for _, cumulative := range []bool{false, true} {
			t.Run(fmtTransportCase(recoverDB, cumulative), func(t *testing.T) {
				source, raw := retentionStore(t)
				first, _, _ := addressThree(t, source, "recipient")
				addressTo(t, source, "other", "PROJ-4")
				other, err := source.InboxList(t.Context(), "other")
				require.NoError(t, err)
				require.Len(t, other, 1)
				if cumulative {
					require.NoError(t, source.InboxAckThrough(t.Context(), "other", other[0].Seq))
				} else {
					// A hook-created recipient can acknowledge a direct event too. Its
					// coverage must not become the direct recipient's coverage.
					_, err = raw.Exec(`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) VALUES('other',?,'2026-11-05T00:00:00Z')`, first)
					require.NoError(t, err)
					require.NoError(t, source.InboxAck(t.Context(), "other", other[0].Seq))
				}
				require.NoError(t, source.AppendAnnotation(t.Context(), "PROJ-1", model.Annotation{ID: "ordinary-comment", Text: "not addressed", Author: "sender"}))
				file := transportExport(t, source)
				if recoverDB {
					var dbPath string
					require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&dbPath))
					result, err := sqlite.Recover(t.Context(), dbPath, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
					require.NoError(t, err)
					file = result.Export
				}
				require.Len(t, file.InboxJournal.Events, 4, "ordinary comment is intentionally excluded")
				dest := newTestStore(t)
				_, err = dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
				require.NoError(t, err)
				unread, err := dest.InboxList(t.Context(), "recipient")
				require.NoError(t, err)
				require.Len(t, unread, 3)
				unread, err = dest.InboxList(t.Context(), "other")
				require.NoError(t, err)
				require.Empty(t, unread)
			})
		}
	}
}

func fmtTransportCase(recovered, cumulative bool) string {
	name := "export-selective"
	if recovered {
		name = "recovery-selective"
	}
	if cumulative {
		name += "-cumulative"
	}
	return name
}

func TestImport_ExistingEventIncomingAck_UpdatesRecipientCoverage(t *testing.T) {
	source := newTestStore(t)
	first, _, _ := addressThree(t, source, "recipient")
	dest := newTestStore(t)
	_, err := dest.Import(t.Context(), transportExport(t, source), sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	require.NoError(t, source.InboxAck(t.Context(), "recipient", first))
	_, err = dest.Import(t.Context(), transportExport(t, source), sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	unread, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, unread, 2)
}

func TestRecover_DatabaseRecipientCumulativeAck_RetainsCoverage(t *testing.T) {
	source, raw := retentionStore(t)
	first, _, last := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAckThrough(t.Context(), "recipient", first))
	require.NoError(t, source.InboxAck(t.Context(), "recipient", last))
	var path string
	require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	want, err := source.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, want[0].EventID, got[0].EventID)
}

func TestRecover_OrdinaryMirrorOnly_ReportsUnavailableJournal(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	board, err := source.Export(t.Context(), "", "test")
	require.NoError(t, err)
	raw, err := json.Marshal(board)
	require.NoError(t, err)
	dir := t.TempDir()
	path := filepath.Join(dir, "tasks.json")
	require.NoError(t, os.WriteFile(path, raw, 0600))
	result, err := sqlite.Recover(t.Context(), filepath.Join(dir, "absent.db"), path, "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.Equal(t, 3, result.Export.NodeCount)
	require.Nil(t, result.Export.InboxJournal)
	require.Contains(t, strings.Join(result.Notes, " "), "lost event count unknown")
}
