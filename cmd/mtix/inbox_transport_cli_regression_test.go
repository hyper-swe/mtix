// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestRunInbox_RestoredMail_ShownWithoutIncludeHeld(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "restored body visible in plain inbox")
	file, err := app.store.ExportWithInbox(t.Context(), "", "test")
	require.NoError(t, err)
	require.NoError(t, sqlite.DiscardLocal(t.Context(), app.store, app.mtixDir))
	_, err = app.store.Import(t.Context(), file, sqlite.ImportModeMerge, false)
	require.NoError(t, err)
	output, err := cliBehaviorOutput(t, func() error { return runInbox("worker", false, 0, "") })
	require.NoError(t, err)
	require.Contains(t, output, "restored body visible in plain inbox")
	require.NotContains(t, output, "inbox empty")
}

func TestRunRecoverFrom_StalePrivateExport_PreservesNewerMirrorAndMail(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "mail only in private export")
	output, err := cliBehaviorOutput(t, func() error { return runExport("json") })
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "private.json")
	require.NoError(t, os.WriteFile(path, []byte(output), 0600))
	require.NoError(t, runCreate("newer mirror-only task", "", "", 3, "", "", "", "", ""))
	require.NoError(t, app.syncSvc.AutoExport(t.Context(), app.mtixDir))
	require.NoError(t, app.store.Close())
	dbPath := filepath.Join(app.mtixDir, "data", "mtix.db")
	require.NoError(t, os.Rename(dbPath, dbPath+".saved"))
	recovered, err := runRecoverFrom(path)
	require.NoError(t, err)
	raw, err := os.ReadFile(recovered)
	require.NoError(t, err)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &data))
	assert.Equal(t, 2, data.NodeCount, "private backup must not replace the current board mirror")
	require.NotNil(t, data.InboxJournal, "private export supplements the journal-free mirror")
	require.Len(t, data.InboxJournal.Events, 1)
	assert.Contains(t, data.InboxJournal.Events[0].Payload, "mail only in private export")
	require.NoError(t, sqlite.ValidateExport(&data))
	info, err := os.Stat(recovered)
	require.NoError(t, err)
	if runtime.GOOS != "windows" {
		assert.Equal(t, os.FileMode(0600), info.Mode().Perm())
	}
}

func TestNewExportCmd_BoardOnly_SanitizedMirrorProcedureOmitsMail(t *testing.T) {
	initTestApp(t)
	envelopeCLIEvents(t, "private mail")
	cmd := newExportCmd()
	require.NoError(t, cmd.ParseFlags([]string{"--board-only"}))
	output, err := cliBehaviorOutput(t, func() error { return cmd.RunE(cmd, nil) })
	require.NoError(t, err)
	clean := filepath.Join(app.mtixDir, "tasks-clean.json")
	require.NoError(t, os.WriteFile(clean, []byte(output), 0600))
	require.NoError(t, os.Rename(clean, filepath.Join(app.mtixDir, "tasks.json")))
	mirror, err := os.ReadFile(filepath.Join(app.mtixDir, "tasks.json"))
	require.NoError(t, err)
	require.NotContains(t, string(mirror), "inbox_journal")
	var file sqlite.ExportData
	require.NoError(t, json.Unmarshal(mirror, &file))
	require.NoError(t, sqlite.ValidateExport(&file))
}

func TestRunExport_MalformedComment_ReportsUnknownAddressedLoss(t *testing.T) {
	for _, payload := range []string{"{", `{"to":7}`} {
		t.Run(payload, func(t *testing.T) {
			initTestApp(t)
			events := envelopeCLIEvents(t, "damaged mail", "valid mail")
			_, err := app.store.WriteDB().ExecContext(t.Context(), `UPDATE sync_events SET payload=? WHERE event_id=?`, payload, events[0].EventID)
			require.NoError(t, err)
			var output string
			note, err := captureStderrOf(t, func() error {
				var exportErr error
				output, exportErr = cliBehaviorOutput(t, func() error { return runExport("json") })
				return exportErr
			})
			require.NoError(t, err)
			var data sqlite.ExportData
			require.NoError(t, json.Unmarshal([]byte(output), &data))
			require.Len(t, data.InboxJournal.Events, 1)
			require.Contains(t, note, "unreadable")
			require.Contains(t, note, "unknown")
		})
	}
}

func TestRunExport_UncarriedHookDelivery_ReportsUnreadCount(t *testing.T) {
	initTestApp(t)
	events := envelopeCLIEvents(t, "direct mail")
	require.NoError(t, app.store.RecordInboxDelivery(t.Context(), "hooked", events[0].Seq, "test-hook"))
	note, err := captureStderrOf(t, func() error {
		_, exportErr := cliBehaviorOutput(t, func() error { return runExport("json") })
		return exportErr
	})
	require.NoError(t, err)
	require.Contains(t, note, "1 unacknowledged")
	require.Contains(t, note, "not carried")
}

func TestRunExport_DiagnosticFailure_StillReturnsCompleteSnapshot(t *testing.T) {
	for _, failure := range []string{"count", "stderr"} {
		t.Run(failure, func(t *testing.T) {
			initTestApp(t)
			envelopeCLIEvents(t, "mail")
			if failure == "count" {
				_, err := app.store.WriteDB().ExecContext(t.Context(), `DROP TABLE agent_inbox_cursor`)
				require.NoError(t, err)
			} else {
				old := os.Stderr
				closed, err := os.Create(filepath.Join(t.TempDir(), "closed"))
				require.NoError(t, err)
				require.NoError(t, closed.Close())
				os.Stderr = closed
				t.Cleanup(func() { os.Stderr = old })
			}
			output, err := cliBehaviorOutput(t, func() error { return runExportWithInbox("json", false) })
			require.NoError(t, err, "note failure cannot fail a successfully written export")
			var data sqlite.ExportData
			require.NoError(t, json.Unmarshal([]byte(output), &data))
			require.NoError(t, sqlite.ValidateExport(&data))
			require.Equal(t, 1, data.NodeCount)
		})
	}
}
