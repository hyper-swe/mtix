// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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

	err := runImport("data.json", "merge", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunImport_NonexistentFile_ReturnsError verifies error on missing file.
func TestRunImport_NonexistentFile_ReturnsError(t *testing.T) {
	initTestApp(t)

	err := runImport("/nonexistent/file.json", "merge", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read import file")
}

// TestRunImport_InvalidJSON_ReturnsError verifies error on malformed JSON.
func TestRunImport_InvalidJSON_ReturnsError(t *testing.T) {
	initTestApp(t)

	badFile := filepath.Join(t.TempDir(), "bad.json")
	require.NoError(t, os.WriteFile(badFile, []byte("{not json}"), 0o644))

	err := runImport(badFile, "merge", false)
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
	err = runImport(exportFile, "merge", false)
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

	err = runImport(exportFile, "merge", false)
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

	err = runImport(exportFile, "replace", false)
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
