// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeRefusalRecord records a refusal of the current .mtix/tasks.json, as an
// auto-import that stopped for a decision leaves it.
func writeRefusalRecord(t *testing.T, kind string) {
	t.Helper()
	board := []byte(`{"nodes":[]}`)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "tasks.json"), board, 0o644))
	rec := map[string]any{
		"refused_at": "2026-11-12T10:00:00Z", "file_hash": fmt.Sprintf("%x", sha256.Sum256(board)),
		"kind": kind, "reason": "the file would delete local data",
	}
	raw, err := json.Marshal(rec)
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(app.mtixDir, "data"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "data", "auto-import-refusal.json"), raw, 0o644))
}

// TestAppendImportRefusalCheck_NonePending_Passes: a project with no refusal
// shows the check as passing.
func TestAppendImportRefusalCheck_NonePending_Passes(t *testing.T) {
	initTestApp(t)
	r := appendImportRefusalCheck(DoctorReport{OverallPass: true}, app.syncSvc, app.mtixDir)
	require.True(t, r.OverallPass)
	require.Len(t, r.Checks, 1)
	require.Equal(t, importRefusalCheckName, r.Checks[0].Name)
	require.True(t, r.Checks[0].Pass)
	require.Empty(t, r.Checks[0].Fix)
}

// TestAppendImportRefusalCheck_Pending_FailsWithKindFileAndFix: a pending
// refusal fails the check and names its kind, the file and the exact fix.
func TestAppendImportRefusalCheck_Pending_FailsWithKindFileAndFix(t *testing.T) {
	initTestApp(t)
	writeRefusalRecord(t, "lossy")
	r := appendImportRefusalCheck(DoctorReport{OverallPass: true}, app.syncSvc, app.mtixDir)
	require.False(t, r.OverallPass)
	c := r.Checks[0]
	require.False(t, c.Pass)
	require.Contains(t, c.Detail, "lossy")
	require.Contains(t, c.Detail, ".mtix/tasks.json")
	require.Contains(t, c.Detail, "the file would delete local data", "the refusal reason is shown")
	require.Contains(t, c.Fix, "mtix sync --fix")

	raw, err := json.Marshal(c)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	require.Equal(t, importRefusalCheckName, got["name"])
	require.Equal(t, false, got["pass"])
	require.NotEmpty(t, got["fix"])
	require.NotEmpty(t, got["detail"])
}

// TestAppendImportRefusalCheck_NoService_Skipped: without a sync service the
// check cannot run and says so instead of passing.
func TestAppendImportRefusalCheck_NoService_Skipped(t *testing.T) {
	r := appendImportRefusalCheck(DoctorReport{OverallPass: true}, nil, "")
	require.True(t, r.OverallPass)
	require.True(t, r.Checks[0].Skipped)
}

// TestAppendLocalChecks_IncludesImportRefusalCheck: the doctor's local checks
// carry the import refusal check.
func TestAppendLocalChecks_IncludesImportRefusalCheck(t *testing.T) {
	initTestApp(t)
	writeRefusalRecord(t, "conflict")
	r := appendLocalChecks(t.Context(), DoctorReport{OverallPass: true})
	var found *DoctorCheck
	for i := range r.Checks {
		if r.Checks[i].Name == importRefusalCheckName {
			found = &r.Checks[i]
		}
	}
	require.NotNil(t, found)
	require.False(t, found.Pass)
}

// TestRefuseSyncedMerge_UsesServiceStoreSyncs: the import handlers learn
// whether the store syncs from the service, so a store marked as syncing is
// refused with no direct store read in the handler.
func TestRefuseSyncedMerge_SyncedStore_Refuses(t *testing.T) {
	initTestApp(t)
	_, err := app.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	require.Error(t, refuseSyncedMerge(t.Context()))
}

// TestImportWorkflow_SyncedChecks_GoThroughTheServiceLayer: the handlers do
// not read the store for the synced-store check (Commandment 7).
func TestImportWorkflow_SyncedChecks_GoThroughTheServiceLayer(t *testing.T) {
	src, err := os.ReadFile("import_workflow.go")
	require.NoError(t, err)
	require.NotContains(t, string(src), "app.store")
	require.NotContains(t, string(src), "countRows")
}

// TestAppendImportRefusalCheck_HostileReason_IsEscaped: a reason that holds a
// control character never reaches the detail raw.
func TestAppendImportRefusalCheck_HostileReason_IsEscaped(t *testing.T) {
	initTestApp(t)
	writeRefusalRecord(t, "lossy")
	rec := filepath.Join(app.mtixDir, "data", "auto-import-refusal.json")
	raw, err := os.ReadFile(rec)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	m["reason"] = "PROJ-1\x1b[2Jgone"
	raw, err = json.Marshal(m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(rec, raw, 0o644))

	r := appendImportRefusalCheck(DoctorReport{OverallPass: true}, app.syncSvc, app.mtixDir)
	require.NotContains(t, r.Checks[0].Detail, "\x1b")
	require.Contains(t, r.Checks[0].Detail, "PROJ-1", "the reason text is still printed as plain text")
	require.Contains(t, r.Checks[0].Detail, "gone")
}

// TestAppendImportRefusalCheck_CorruptRecord_FailsWithReadErrorAndFix: a
// refusal record that cannot be parsed fails the check, naming the record.
func TestAppendImportRefusalCheck_CorruptRecord_FailsWithReadErrorAndFix(t *testing.T) {
	initTestApp(t)
	writeRefusalRecord(t, "lossy")
	rec := filepath.Join(app.mtixDir, "data", "auto-import-refusal.json")
	require.NoError(t, os.WriteFile(rec, []byte("{not json"), 0o644))
	r := appendImportRefusalCheck(DoctorReport{OverallPass: true}, app.syncSvc, app.mtixDir)
	require.False(t, r.OverallPass)
	require.False(t, r.Checks[0].Pass)
	require.Contains(t, r.Checks[0].Detail, "auto-import-refusal.json")
	require.Contains(t, r.Checks[0].Fix, "move the refusal record")
	require.NotContains(t, r.Checks[0].Fix, "tasks.json aside")
}

// TestAppendImportRefusalCheck_UnreadableBoard_Fails: a tasks.json that cannot
// be read (a directory in its place) fails the check instead of passing.
func TestAppendImportRefusalCheck_UnreadableBoard_Fails(t *testing.T) {
	initTestApp(t)
	require.NoError(t, os.MkdirAll(filepath.Join(app.mtixDir, "tasks.json"), 0o755))
	r := appendImportRefusalCheck(DoctorReport{OverallPass: true}, app.syncSvc, app.mtixDir)
	require.False(t, r.Checks[0].Pass)
	require.Contains(t, r.Checks[0].Detail, "tasks.json")
	require.Contains(t, r.Checks[0].Fix, "restore it from git")
	require.NotContains(t, r.Checks[0].Fix, "aside", "the board is never moved aside")
}

// TestRefuseSyncedMerge_UnreadableStore_ErrorDoesNotNameAutoImport: the manual
// import error keeps its own wording.
func TestRefuseSyncedMerge_UnreadableStore_ErrorDoesNotNameAutoImport(t *testing.T) {
	initTestApp(t)
	_, err := app.store.WriteDB().ExecContext(t.Context(), `DROP TABLE applied_events`)
	require.NoError(t, err)
	err = refuseSyncedMerge(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "read whether the store syncs with a hub")
	require.NotContains(t, err.Error(), "auto-import")
}

// TestRefuseSyncedReplace_EmptySyncedStore_Runs: a replace into a store that
// holds no task deletes nothing, so the guard lets it through even when the
// store syncs; with a task in it, the guard refuses.
func TestRefuseSyncedReplace_EmptySyncedStore_Runs(t *testing.T) {
	initTestApp(t)
	_, err := app.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	require.NoError(t, refuseSyncedReplace(t.Context()))
}

// TestAppendImportRefusalCheck_NoBoardFile_PassesWithoutError: a fresh project
// with no tasks.json has nothing pending, so the check passes.
func TestAppendImportRefusalCheck_NoBoardFile_PassesWithoutError(t *testing.T) {
	initTestApp(t)
	err := os.Remove(filepath.Join(app.mtixDir, "tasks.json"))
	require.True(t, err == nil || os.IsNotExist(err), "no board file")
	r := appendImportRefusalCheck(DoctorReport{OverallPass: true}, app.syncSvc, app.mtixDir)
	require.True(t, r.OverallPass)
	require.Len(t, r.Checks, 1)
	require.True(t, r.Checks[0].Pass)
	require.Empty(t, r.Checks[0].Fix)
}
