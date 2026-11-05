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

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestRecover_TamperedMirrorJournal_RejectsPrivateMail(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	file := transportExport(t, source)
	file.InboxJournal.Events[0].Payload = `{"to":"recipient","body":"tampered"}`
	raw, err := json.Marshal(file)
	require.NoError(t, err)
	mirror := filepath.Join(t.TempDir(), "mirror.json")
	require.NoError(t, os.WriteFile(mirror, raw, 0600))
	result, err := sqlite.Recover(t.Context(), filepath.Join(t.TempDir(), "absent.db"), mirror, "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.Nil(t, result.Export.InboxJournal)
	require.Contains(t, strings.Join(result.Notes, " "), "export inbox journal rejected")
	require.NoError(t, sqlite.ValidateExport(result.Export))
}

func TestRecover_ConflictingDuplicateInbox_DoesNotAdoptOtherAck(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	for _, query := range []string{
		`CREATE TABLE journal_without_index AS SELECT * FROM sync_events`,
		`DROP TABLE sync_events`,
		`ALTER TABLE journal_without_index RENAME TO sync_events`,
		`INSERT INTO sync_events SELECT * FROM sync_events WHERE rowid=(SELECT MIN(rowid) FROM sync_events WHERE op_type='comment')`,
		`UPDATE sync_events SET payload=json_set(payload,'$.body','conflicting copy') WHERE rowid=(SELECT MAX(rowid) FROM sync_events)`,
		`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) SELECT 'recipient',MAX(rowid),'2026-11-05T01:02:03Z' FROM sync_events`,
	} {
		_, err := raw.Exec(query)
		require.NoError(t, err)
	}
	var path string
	require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.Len(t, result.Export.InboxJournal.Events, 3)
	require.False(t, result.Export.InboxJournal.Events[0].Acked)
	require.NotContains(t, result.Export.InboxJournal.Events[0].Payload, "conflicting copy")
	require.Contains(t, strings.Join(result.Notes, " "), "conflicting duplicate inbox identity omitted")
}

func TestExportWithInbox_OversizedSender_ReportsTransportBound(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	_, err := raw.Exec(`UPDATE sync_events SET payload=json_set(payload,'$.author_id',?) WHERE op_type='comment'`, strings.Repeat("a", 65))
	require.NoError(t, err)
	mail, err := source.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, mail, 3)
	file := transportExport(t, source)
	require.Empty(t, file.InboxJournal.Events)
	require.Contains(t, strings.Join(file.InboxJournal.Notes, " "), "3 addressed inbox event(s) outside the 64-byte transport actor bounds")
	require.NotContains(t, strings.Join(file.InboxJournal.Notes, " "), "unreadable")
}
