// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: duplicate identity makes every recovered acknowledgment untrusted.
func TestRecover_DuplicateWithLaterAck_DiscardsAllCoverage(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "recipient")
	for _, statement := range []string{
		`CREATE TABLE journal_without_index AS SELECT * FROM sync_events`,
		`DROP TABLE sync_events`,
		`ALTER TABLE journal_without_index RENAME TO sync_events`,
		`INSERT INTO sync_events SELECT * FROM sync_events WHERE rowid=(SELECT MIN(rowid) FROM sync_events WHERE op_type='comment')`,
		`INSERT INTO agent_inbox_ack(agent_id,event_seq,acked_at) SELECT 'recipient',MAX(rowid),'2026-11-05T01:02:03Z' FROM sync_events`,
	} {
		_, err := raw.Exec(statement)
		require.NoError(t, err)
	}
	var path string
	require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.NoError(t, sqlite.ValidateExport(result.Export))
	require.Len(t, result.Export.InboxJournal.Events, 3)
	first := result.Export.InboxJournal.Events[0]
	require.False(t, first.Acked)
	require.Empty(t, first.AckedAt)
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 3)
}

func TestRecover_InvalidAckTimestamp_KeepsMailUnacknowledged(t *testing.T) {
	source, raw := retentionStore(t)
	first, _, _ := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAck(t.Context(), "recipient", first))
	_, err := raw.Exec(`UPDATE agent_inbox_ack SET acked_at='broken'`)
	require.NoError(t, err)
	var path string
	require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
	require.NoError(t, err)
	require.NoError(t, sqlite.ValidateExport(result.Export))
	require.Len(t, result.Export.InboxJournal.Events, 3)
	require.False(t, result.Export.InboxJournal.Events[0].Acked)
	require.Empty(t, result.Export.InboxJournal.Events[0].AckedAt)
	require.Contains(t, strings.Join(result.Notes, " "), "acknowledgment coverage unreadable for 1")
}

func TestImport_AcknowledgedJournal_PreservesExactAckTime(t *testing.T) {
	source, raw := retentionStore(t)
	first, _, _ := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAck(t.Context(), "recipient", first))
	const stamp = "2026-11-05T01:02:03.123456789Z"
	_, err := raw.Exec(`UPDATE agent_inbox_ack SET acked_at=?`, stamp)
	require.NoError(t, err)
	dest, destRaw := retentionStore(t)
	file := transportExport(t, source)
	_, err = dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	var got string
	require.NoError(t, destRaw.QueryRow(`SELECT k.acked_at FROM agent_inbox_ack k JOIN sync_events e ON e.rowid=k.event_seq WHERE e.event_id=? AND k.agent_id=?`, file.InboxJournal.Events[0].EventID, "recipient").Scan(&got))
	require.Equal(t, stamp, got)
}

func TestImport_SyncedStoreDropsRestoredOnlyTask_AllowsReplace(t *testing.T) {
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	dest, raw := retentionStore(t)
	file := transportExport(t, source)
	_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	// A hub-contact mark must not turn restored mail into replication evidence.
	_, err = raw.Exec(`INSERT INTO board_flags(key,value,set_at,source) VALUES('hub_connected','1','2026-11-05T00:00:00Z','test')`)
	require.NoError(t, err)
	file = withoutExportNode(file, "PROJ-2")
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	_, err = dest.Import(t.Context(), file, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 3)
}
