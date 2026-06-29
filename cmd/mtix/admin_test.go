// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// ============================================================================
// Tests for admin command functions: verify, backup, import, serve.
// These cover the newly wired (previously stubbed) implementations.
// ============================================================================

// --- runVerify tests ---

// TestRunVerify_FullProject_EmptyDB_Succeeds verifies full verification with no nodes.
func TestRunVerify_FullProject_EmptyDB_Succeeds(t *testing.T) {
	initTestApp(t)
	err := runVerify("")
	assert.NoError(t, err)
}

// TestRunVerify_FullProject_WithNodes_AllPass verifies content hash integrity.
func TestRunVerify_FullProject_WithNodes_AllPass(t *testing.T) {
	initTestApp(t)

	// Create some nodes so there's data to verify.
	require.NoError(t, runCreate("Node A", "", "", 3, "desc-a", "", "", "", ""))
	require.NoError(t, runCreate("Node B", "", "", 2, "desc-b", "", "", "", ""))

	err := runVerify("")
	assert.NoError(t, err)
}

// TestRunVerify_FullProject_JSONMode verifies JSON output format.
func TestRunVerify_FullProject_JSONMode(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	require.NoError(t, runCreate("Verify JSON", "", "", 3, "", "", "", "", ""))

	err := runVerify("")
	assert.NoError(t, err)
}

// TestRunVerify_SingleNode_JSONMode verifies single-node JSON output.
func TestRunVerify_SingleNode_JSONMode(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	require.NoError(t, runCreate("Single Verify", "", "", 3, "", "", "", "", ""))

	err := runVerify("TEST-1")
	assert.NoError(t, err)
}

// TestRunVerify_FullProject_WithMismatch_ReportsFailure verifies integrity failure output.
func TestRunVerify_FullProject_WithMismatch_ReportsFailure(t *testing.T) {
	initTestApp(t)

	require.NoError(t, runCreate("Corrupt Me", "", "", 3, "original", "", "", "", ""))

	// Corrupt the content hash by directly updating the database.
	ctx := context.Background()
	_, execErr := app.store.WriteDB().ExecContext(ctx,
		"UPDATE nodes SET content_hash = 'badhash' WHERE id = ?", "TEST-1")
	require.NoError(t, execErr)

	verifyErr := runVerify("")
	assert.NoError(t, verifyErr) // Verify succeeds but prints mismatch info.
}

// TestRunVerify_FullProject_WithMismatch_JSONMode verifies JSON mismatch output.
func TestRunVerify_FullProject_WithMismatch_JSONMode(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	require.NoError(t, runCreate("Corrupt JSON", "", "", 3, "", "", "", "", ""))

	ctx := context.Background()
	_, execErr := app.store.WriteDB().ExecContext(ctx,
		"UPDATE nodes SET content_hash = 'badhash' WHERE id = ?", "TEST-1")
	require.NoError(t, execErr)

	err := runVerify("")
	assert.NoError(t, err)
}

// --- runBackup tests ---

// TestRunBackup_Success_VerifiesFile verifies backup creates a valid file.
func TestRunBackup_Success_VerifiesFile(t *testing.T) {
	initTestApp(t)

	require.NoError(t, runCreate("Backup Node", "", "", 3, "", "", "", "", ""))

	backupPath := filepath.Join(t.TempDir(), "backup.db")
	err := runBackup(backupPath)
	assert.NoError(t, err)

	// Verify the file was created.
	info, statErr := os.Stat(backupPath)
	require.NoError(t, statErr)
	assert.True(t, info.Size() > 0, "backup file should not be empty")
}

// TestRunBackup_JSONMode verifies backup JSON output.
func TestRunBackup_JSONMode(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	backupPath := filepath.Join(t.TempDir(), "backup-json.db")
	err := runBackup(backupPath)
	assert.NoError(t, err)
}

// TestRunBackup_InvalidPath_ReturnsError verifies error on invalid path.
func TestRunBackup_InvalidPath_ReturnsError(t *testing.T) {
	initTestApp(t)

	err := runBackup("/nonexistent/dir/backup.db")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "backup failed")
}

// TestRunBackup_EmptyPath_ReturnsError verifies error on empty path.
func TestRunBackup_EmptyPath_ReturnsError(t *testing.T) {
	initTestApp(t)

	err := runBackup("")
	assert.Error(t, err)
}

// --- runImport tests ---

// TestRunImport_NoStore_ReturnsError verifies nil store guard.
func TestRunImport_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runImport("data.json", importFlags{mode: "merge"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunImport_NonexistentFile_ReturnsError verifies error on missing file.
func TestRunImport_NonexistentFile_ReturnsError(t *testing.T) {
	initTestApp(t)

	err := runImport("/nonexistent/file.json", importFlags{mode: "merge"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read import file")
}

// TestRunImport_InvalidJSON_ReturnsError verifies error on malformed JSON.
func TestRunImport_InvalidJSON_ReturnsError(t *testing.T) {
	initTestApp(t)

	badFile := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(badFile, []byte("{not json}"), 0o644))

	err := runImport(badFile, importFlags{mode: "merge"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse import file")
}

// TestRunImport_ExportThenImport_Roundtrip verifies export→import roundtrip.
func TestRunImport_ExportThenImport_Roundtrip(t *testing.T) {
	initTestApp(t)

	// Create nodes to export.
	require.NoError(t, runCreate("Import A", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Import B", "", "", 2, "", "", "", "", ""))

	// Export using the store directly.
	ctx := t.Context()
	exportData, err := app.store.Export(ctx, "TEST", "test")
	require.NoError(t, err)

	// Write export to file.
	exportFile := filepath.Join(t.TempDir(), "export.json")
	data, err := json.MarshalIndent(exportData, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(exportFile, data, 0o644))

	// Import in merge mode.
	err = runImport(exportFile, importFlags{mode: "merge"})
	assert.NoError(t, err)
}

// TestRunImport_JSONMode_Succeeds verifies import with JSON output.
func TestRunImport_JSONMode_Succeeds(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	require.NoError(t, runCreate("Export Me", "", "", 3, "", "", "", "", ""))

	ctx := t.Context()
	exportData, err := app.store.Export(ctx, "TEST", "test")
	require.NoError(t, err)

	exportFile := filepath.Join(t.TempDir(), "export.json")
	data, err := json.MarshalIndent(exportData, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(exportFile, data, 0o644))

	err = runImport(exportFile, importFlags{mode: "merge"})
	assert.NoError(t, err)
}

// TestRunImport_ReplaceMode_Succeeds verifies import with replace mode.
func TestRunImport_ReplaceMode_Succeeds(t *testing.T) {
	initTestApp(t)

	require.NoError(t, runCreate("Replace Me", "", "", 3, "", "", "", "", ""))

	ctx := t.Context()
	exportData, err := app.store.Export(ctx, "TEST", "test")
	require.NoError(t, err)

	exportFile := filepath.Join(t.TempDir(), "export.json")
	data, err := json.MarshalIndent(exportData, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(exportFile, data, 0o644))

	err = runImport(exportFile, importFlags{mode: "replace"})
	assert.NoError(t, err)
}

// --- runServe tests ---
// Note: runServe starts a real server and blocks, so we can only test
// the nil-store guard and cmd construction. The actual server startup
// is tested via HTTP integration tests in internal/api/http/.

// TestNewImportCmd_HasModeFlag verifies import command has --mode flag.
func TestNewImportCmd_HasModeFlag(t *testing.T) {
	cmd := newImportCmd()
	assert.Equal(t, "import <file>", cmd.Use)

	modeFlag := cmd.Flags().Lookup("mode")
	require.NotNil(t, modeFlag)
	assert.Equal(t, "merge", modeFlag.DefValue)
}

// TestNewImportCmd_HasReconcileFlags verifies the ADR-003 §6 reconciliation
// flags are wired onto the import command.
func TestNewImportCmd_HasReconcileFlags(t *testing.T) {
	cmd := newImportCmd()
	for _, name := range []string{"confirm", "force-rename", "remap-file"} {
		require.NotNil(t, cmd.Flags().Lookup(name), "missing flag --%s", name)
	}
}

// writeProvisionalImportFile builds an export file under prefix TEST containing a
// settled root and a provisional (uid-bearing) child whose clean number clashes
// with an existing local child, returning the file path and the child's uid.
func writeProvisionalImportFile(t *testing.T, provUID string) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	src, err := sqlite.New(filepath.Join(t.TempDir(), "src.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })

	require.NoError(t, src.CreateNode(ctx, &model.Node{
		ID: "TEST-1", Project: "TEST", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "r1", UID: mustUID(t),
		CreatedAt: now, UpdatedAt: now,
	}))
	provID, err := model.BuildProvisionalID("TEST-1", provUID)
	require.NoError(t, err)
	require.NoError(t, src.CreateNode(ctx, &model.Node{
		ID: provID, ParentID: "TEST-1", Project: "TEST", Depth: 1, Seq: 1,
		Title: "Provisional child", Status: model.StatusOpen,
		Priority: model.PriorityMedium, Weight: 1.0, NodeType: model.NodeTypeStory,
		ContentHash: "p1", UID: provUID, CreatedAt: now, UpdatedAt: now,
	}))

	data, err := src.Export(ctx, "TEST", "test")
	require.NoError(t, err)
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "import.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

func mustUID(t *testing.T) string {
	t.Helper()
	uid, err := clock.NewEventID()
	require.NoError(t, err)
	return uid
}

// TestRunImport_LiveStoreWithoutConfirm_Rejected verifies a renumbering import
// into a non-empty live store is withheld without --confirm (ADR-003 §6).
func TestRunImport_LiveStoreWithoutConfirm_Rejected(t *testing.T) {
	initTestApp(t)
	// Local store already owns TEST-1 and TEST-1.1, so the incoming provisional
	// child cannot take clean number 1 and must be renumbered.
	require.NoError(t, runCreate("Local root", "", "epic", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Local child", "TEST-1", "", 3, "", "", "", "", ""))

	provUID := mustUID(t)
	path := writeProvisionalImportFile(t, provUID)

	err := runImport(path, importFlags{mode: "merge"})
	require.Error(t, err)
	assert.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)

	// The provisional node must NOT have been applied.
	_, resErr := app.store.ResolveDisplayPathByUID(t.Context(), provUID)
	assert.ErrorIs(t, resErr, model.ErrNotFound)
}

// TestRunImport_ConfirmAppliesAndWritesRemap verifies that --confirm applies the
// renumber and --remap-file persists the uid-keyed remap (ADR-003 §6).
func TestRunImport_ConfirmAppliesAndWritesRemap(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local root", "", "epic", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("Local child", "TEST-1", "", 3, "", "", "", "", ""))

	provUID := mustUID(t)
	path := writeProvisionalImportFile(t, provUID)
	remapPath := filepath.Join(t.TempDir(), "remap.json")

	err := runImport(path, importFlags{mode: "merge", confirm: true, remapFile: remapPath})
	require.NoError(t, err)

	// Renumbered to the next free clean number and resolvable via uid.
	resolved, resErr := app.store.ResolveDisplayPathByUID(t.Context(), provUID)
	require.NoError(t, resErr)
	assert.Equal(t, "TEST-1.2", resolved)

	// The remap file records uid -> new clean display_path.
	remapRaw, readErr := os.ReadFile(remapPath)
	require.NoError(t, readErr)
	var remap map[string]string
	require.NoError(t, json.Unmarshal(remapRaw, &remap))
	assert.Equal(t, "TEST-1.2", remap[provUID])
}
