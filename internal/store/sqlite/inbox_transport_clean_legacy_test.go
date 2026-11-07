// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"bytes"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-19.1: clean recovery retains coverage for mail supplied only by an export.
func TestRecover_CleanExportOnlyMail_RetainsCoverageAndNote(t *testing.T) {
	source, _ := retentionStore(t)
	addressTo(t, source, "recipient", "PROJ-1")
	sent, err := source.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, sent, 1)
	require.NoError(t, source.InboxAck(t.Context(), "recipient", sent[0].Seq))
	file := transportExport(t, source)
	clean, raw := retentionStore(t)
	addressTo(t, clean, "recipient", "PROJ-1")
	var path string
	require.NoError(t, raw.QueryRow(`SELECT file FROM pragma_database_list WHERE name='main'`).Scan(&path))
	result, err := sqlite.Recover(t.Context(), path, filepath.Join(t.TempDir(), "absent.json"), "test", sqlite.RecoverOptions{Logger: slog.New(slog.NewTextHandler(io.Discard, nil)), InboxSources: []*sqlite.ExportData{file}})
	require.NoError(t, err)
	require.False(t, result.InboxIncomplete)
	require.NoError(t, sqlite.ValidateExport(result.Export))
	require.Len(t, result.Export.InboxJournal.Events, 2)
	found := false
	for _, event := range result.Export.InboxJournal.Events {
		if event.EventID == sent[0].EventID {
			found = true
			require.True(t, event.Acked)
			require.NotEmpty(t, event.AckedAt)
		}
	}
	require.True(t, found)
	require.Contains(t, strings.Join(result.Notes, " "), "acknowledgment coverage retained after a completed integrity check and complete salvage")
	dest, _ := retentionStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	unread, err := dest.InboxList(t.Context(), "recipient")
	require.NoError(t, err)
	require.Len(t, unread, 1)
	require.NotEqual(t, sent[0].EventID, unread[0].EventID)
}

// FR-19.1: the lead accepts this documented limit on older, unindexed stores.
func TestRecover_LegacyRecipientFlip_DocumentedLimit(t *testing.T) {
	source, raw := retentionStore(t)
	addressThree(t, source, "bob")
	_, err := raw.Exec(`DROP INDEX idx_sync_events_comment_addressee`)
	require.NoError(t, err)
	_, err = raw.Exec(`INSERT INTO agent_inbox_cursor(agent_id,cursor) VALUES('bof',999)`)
	require.NoError(t, err)
	path, _, _ := transportBackupPage(t, source, "sync_events")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	needle := []byte(`"to":"bob"`)
	at := bytes.Index(data, needle)
	require.GreaterOrEqual(t, at, 0)
	data[at+len(needle)-2] ^= 4 // bob -> bof, one bit in the JSON recipient.
	require.NoError(t, os.WriteFile(path, data, 0600))
	result := recoverDamageMail(t, path)
	require.False(t, result.InboxIncomplete, "without the addressee index this change is structurally valid")
	require.Len(t, result.Export.InboxJournal.Events, 3)
	dest, _ := retentionStore(t)
	_, err = dest.Import(t.Context(), result.Export, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	mail, err := dest.InboxList(t.Context(), "bob")
	require.NoError(t, err)
	require.Len(t, mail, 2)
	moved, err := dest.InboxList(t.Context(), "bof")
	require.NoError(t, err)
	require.Empty(t, moved, "the changed recipient's cursor can hide this mail; docs must name this limit")
}
