// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/workflow"
)

// FR-19.1 / MTIX-112.2.1: healthy free-form sender identities survive a move.
func TestExportWithInbox_DisplaySender_PreservesMailThroughImportAndRecover(t *testing.T) {
	for _, author := range []string{"Opus", "agent.one", "Opus Reviewer", "codex/lane-1"} {
		t.Run(author, func(t *testing.T) {
			source, raw := retentionStore(t)
			addressThree(t, source, "recipient")
			_, err := raw.Exec(`UPDATE sync_events SET payload=json_set(payload,'$.author_id',?) WHERE op_type='comment'`, author)
			require.NoError(t, err)
			before, err := source.InboxList(t.Context(), "recipient")
			require.NoError(t, err)
			require.Len(t, before, 3)
			data := transportExport(t, source)
			require.Len(t, data.InboxJournal.Events, 3)
			require.Empty(t, data.InboxJournal.Notes)
			var path string
			require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
			recovered, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "missing.json"), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
			require.NoError(t, err)
			for _, file := range []*sqlite.ExportData{data, recovered.Export} {
				dest := newTestStore(t)
				_, err = dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
				require.NoError(t, err)
				got, err := dest.InboxList(t.Context(), "recipient")
				require.NoError(t, err)
				require.Len(t, got, len(before))
				for i := range before {
					require.Equal(t, before[i].EventID, got[i].EventID)
					require.Equal(t, before[i].Body, got[i].Body)
				}
			}
		})
	}
}

// Damage only an index in a closed snapshot; the original store stays intact.
func TestRecover_DamagedCommentIndex_KeepsReadableMail(t *testing.T) {
	source := newTestStore(t)
	first, _, _ := addressThree(t, source, "recipient")
	require.NoError(t, source.InboxAck(t.Context(), "recipient", first))
	path, root, size := transportBackupPage(t, source, "idx_sync_events_op_type")
	file, err := os.OpenFile(path, os.O_RDWR, 0600)
	require.NoError(t, err)
	_, err = file.WriteAt(bytes.Repeat([]byte{0xff}, size), int64((root-1)*size))
	require.NoError(t, err)
	require.NoError(t, file.Close())
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "missing.json"), "test", slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	require.Equal(t, 3, result.Export.NodeCount)
	require.NotNil(t, result.Export.InboxJournal)
	require.Len(t, result.Export.InboxJournal.Events, 3)
	dest := newTestStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 2)
}

func TestImport_DroppedTaskRetainedMail_LeavesOutboundQueue(t *testing.T) {
	dest, raw := retentionStore(t)
	addressThree(t, dest, "recipient")
	_, err := raw.Exec(`INSERT INTO sync_quarantine(event_id,source,raw_event,reason,first_seen,last_attempt)
 SELECT event_id,'push','{}','held','2026-11-05T00:00:00Z','2026-11-05T00:00:00Z' FROM sync_events WHERE op_type='comment' AND node_id='PROJ-2'`)
	require.NoError(t, err)
	file := withoutExportNode(transportExport(t, dest), "PROJ-2")
	require.NoError(t, sqlite.RecomputeExportChecksum(file))
	_, err = dest.Import(t.Context(), file, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	pending, err := dest.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	require.NotEmpty(t, pending)
	for _, event := range pending {
		require.NotEqual(t, "PROJ-2", event.NodeID)
	}
	relay, err := dest.ReadRelayJournalSince(t.Context(), 0, 100)
	require.NoError(t, err)
	for _, event := range relay {
		require.NotEqual(t, "PROJ-2", event.Event.NodeID)
	}
	got, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, got, 3)
	synced, err := dest.HasSyncedWithHub(t.Context())
	require.NoError(t, err)
	require.False(t, synced)
	var count int
	require.NoError(t, raw.QueryRow(`SELECT COUNT(*) FROM sync_quarantine WHERE source='push'`).Scan(&count))
	require.Zero(t, count)
}

func TestDetectState_RestoredJournal_PreservesBackfillRecommendation(t *testing.T) {
	t.Setenv("MTIX_SYNC_DSN", "test-presence-only")
	source := newTestStore(t)
	addressThree(t, source, "recipient")
	for _, machine := range []string{"", "local-machine"} {
		t.Run(machine, func(t *testing.T) {
			var control workflow.Report
			for _, journal := range []bool{false, true} {
				dest, raw := retentionStore(t)
				file := transportExport(t, source)
				if !journal {
					file.InboxJournal = nil
				}
				_, err := dest.Import(t.Context(), file, sqlite.ImportModeMerge, false)
				require.NoError(t, err)
				_, err = raw.Exec(`INSERT OR REPLACE INTO meta(key,value) VALUES('meta.sync.machine_hash',?)`, machine)
				require.NoError(t, err)
				report, err := workflow.DetectState(t.Context(), raw, "")
				require.NoError(t, err)
				if !journal {
					control = report
				}
				require.Equal(t, control, report)
				require.Equal(t, workflow.StateSyncConfiguredNoHub, report.State)
				require.Zero(t, report.LocalEventCount)
				require.Equal(t, workflow.RecommendForState(control), workflow.RecommendForState(report))
			}
		})
	}
}

// Read page addresses from the backup itself: VACUUM INTO can renumber pages.
func transportBackupPage(t *testing.T, source *sqlite.Store, object string) (string, int, int) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "damaged.db")
	_, err := source.Backup(t.Context(), path)
	require.NoError(t, err)
	db, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	var root, size int
	require.NoError(t, db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE name=?`, object).Scan(&root))
	require.NoError(t, db.QueryRow(`PRAGMA page_size`).Scan(&size))
	require.NoError(t, db.Close())
	return path, root, size
}
