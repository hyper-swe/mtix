// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for the mtix recover command and import --recompute-checksum
// (MTIX-26.5). Written RED-first per TDD-WORKFLOW.md §1.1.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestRunRecover_HealthyProject_WritesImportableExport: happy path — the
// salvage file lands under .mtix/ and carries a valid checksum.
func TestRunRecover_HealthyProject_WritesImportableExport(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("recover cmd fixture", "", "", 3, "", "", "", "", ""))

	outPath, err := runRecover()
	require.NoError(t, err)
	require.FileExists(t, outPath)
	assert.Contains(t, outPath, "recovered-")

	raw, err := os.ReadFile(outPath)
	require.NoError(t, err)
	var data sqlite.ExportData
	require.NoError(t, json.Unmarshal(raw, &data))
	assert.Equal(t, 1, data.NodeCount)

	valid, err := sqlite.VerifyExportChecksum(&data)
	require.NoError(t, err)
	assert.True(t, valid)
}

// TestRunRecover_NoProject_Errors: outside a project there is nothing to
// salvage and the command must say so.
func TestRunRecover_NoProject_Errors(t *testing.T) {
	saveAndResetApp(t)
	tmp := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	require.NoError(t, os.Chdir(tmp))

	_, err = runRecover()
	require.Error(t, err)
}

// TestRunImport_StaleChecksum_RejectedWithoutFlag: the default posture is
// unchanged — integrity verification still gates import.
func TestRunImport_StaleChecksum_RejectedWithoutFlag(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("import checksum fixture", "", "", 3, "", "", "", "", ""))

	path := writeStaleChecksumExport(t)
	err := runImport(path, "replace", false, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "checksum")
}

// TestRunImport_RecomputeChecksum_AcceptsReconstructedFile: the recovery
// path — with the flag, a hand-reconstructed export imports, loudly.
func TestRunImport_RecomputeChecksum_AcceptsReconstructedFile(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("import recompute fixture", "", "", 3, "", "", "", "", ""))

	path := writeStaleChecksumExport(t)
	err := runImport(path, "replace", false, true)
	require.NoError(t, err)
}

// writeStaleChecksumExport exports the current test project, edits a
// title without updating the checksum, and writes it to a temp file —
// the shape of a hand-reconstructed recovery file.
func writeStaleChecksumExport(t *testing.T) string {
	t.Helper()
	data, err := app.store.Export(t.Context(), "TEST", "test")
	require.NoError(t, err)
	require.NotEmpty(t, data.Nodes)
	data.Nodes[0].Title = "edited by hand during recovery"

	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "reconstructed.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}
