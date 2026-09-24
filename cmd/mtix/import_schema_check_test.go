// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// writeExportWithSchema exports the current store, sets its schema_version
// and writes it to a file (the checksum does not cover schema_version, so
// it still verifies). Returns the file path.
func writeExportWithSchema(t *testing.T, schemaVersion string) string {
	t.Helper()
	data, err := app.store.Export(t.Context(), "TEST", "test")
	require.NoError(t, err)
	data.SchemaVersion = schemaVersion
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "export.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}

// TestRunImport_NewerMajorSchema_RefusedAndWritesNothing verifies that
// mtix import applies the FR-15.2g check that auto-import applies: a file
// whose schema_version has a higher major version than this build writes
// is refused with the "newer than supported" message, in both modes, before
// anything is written (MTIX-95.31.1).
func TestRunImport_NewerMajorSchema_RefusedAndWritesNothing(t *testing.T) {
	for _, mode := range []string{"merge", "replace"} {
		t.Run(mode, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("In the file", "", "", 3, "", "", "", "", ""))
			path := writeExportWithSchema(t, "3.0.0")
			require.NoError(t, runCreate("Only in the store", "", "", 3, "", "", "", "", ""))

			err := runImport(path, importFlags{mode: mode})
			require.Error(t, err)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Contains(t, err.Error(), "3.0.0")
			assert.Contains(t, err.Error(), "newer than supported")
			assert.Contains(t, err.Error(), "upgrade mtix")

			node, getErr := app.store.GetNode(t.Context(), "TEST-2")
			require.NoError(t, getErr, "a refused import must leave the store as it was")
			assert.Equal(t, "Only in the store", node.Title)
		})
	}
}

// TestRunImport_SupportedSchemaVersions_Accepted verifies that mtix import
// still reads every version it supports: 1.x files written by clients older
// than 0.5.4, and 2.x files (MTIX-95.31.1, FR-15.2g).
func TestRunImport_SupportedSchemaVersions_Accepted(t *testing.T) {
	for _, version := range []string{"", "1.0.0", "2.0.0", "2.9.1"} {
		t.Run("version "+version, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Round trip", "", "", 3, "", "", "", "", ""))
			path := writeExportWithSchema(t, version)
			assert.NoError(t, runImport(path, importFlags{mode: "merge"}))
		})
	}
}

// TestImportCmd_Refused_LeavesTasksJSONByteIdentical verifies that a refused
// mtix import (here the FR-15.2g schema refusal) changes nothing on disk:
// the command's auto-export is skipped, so .mtix/tasks.json and its stored
// hash stay byte-identical (MTIX-95.31.1 round 3). Before the fix the
// auto-export ran after the refusal and rewrote tasks.json from the store,
// which would overwrite a board just pulled from git.
func TestImportCmd_Refused_LeavesTasksJSONByteIdentical(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("In the store", "", "", 3, "", "", "", "", ""))
	path := writeExportWithSchema(t, "3.0.0")

	tasksPath := filepath.Join(app.mtixDir, "tasks.json")
	hashPath := filepath.Join(app.mtixDir, "data", "sync.sha256")
	pulledBoard := []byte(`{"note": "a board just pulled from git, not yet imported"}` + "\n")
	require.NoError(t, os.WriteFile(tasksPath, pulledBoard, 0o644))
	require.NoError(t, os.WriteFile(hashPath, []byte("stored-hash"), 0o644))

	cmd := newImportCmd()
	cmd.SetArgs([]string{path})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err := cmd.Execute()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newer than supported")

	got, readErr := os.ReadFile(tasksPath)
	require.NoError(t, readErr)
	assert.Equal(t, pulledBoard, got, "a refused import must not rewrite .mtix/tasks.json")
	hash, readErr := os.ReadFile(hashPath)
	require.NoError(t, readErr)
	assert.Equal(t, "stored-hash", string(hash), "a refused import must not touch the stored hash")
}

// TestImportCmd_Applied_StillAutoExports verifies the other side: an import
// that applies still refreshes .mtix/tasks.json (FR-15.3).
func TestImportCmd_Applied_StillAutoExports(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("In the store", "", "", 3, "", "", "", "", ""))
	path := writeExportWithSchema(t, "2.0.0")

	tasksPath := filepath.Join(app.mtixDir, "tasks.json")
	stale := []byte(`{"note": "stale"}` + "\n")
	require.NoError(t, os.WriteFile(tasksPath, stale, 0o644))

	cmd := newImportCmd()
	cmd.SetArgs([]string{path})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	require.NoError(t, cmd.Execute())

	got, err := os.ReadFile(tasksPath)
	require.NoError(t, err)
	assert.NotEqual(t, stale, got, "an applied import re-exports .mtix/tasks.json")
	assert.Contains(t, string(got), `"schema_version": "2.0.0"`)
}

// TestImportCmd_FailsAfterCommit_StillAutoExports verifies the judgment
// behind the skip: an import that fails after its transaction committed
// (rebuilding the sequence counters fails, sqlite.ErrImportIncomplete) did
// change the store, so the board is still re-exported (MTIX-95.31.1).
func TestImportCmd_FailsAfterCommit_StillAutoExports(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("In the store", "", "", 3, "", "", "", "", ""))
	path := writeExportWithSchema(t, "2.0.0")
	// Fixed DDL, test only: make the post-commit sequence rebuild fail.
	_, err := app.store.WriteDB().ExecContext(t.Context(),
		`CREATE TRIGGER test_fail_sequence_rebuild BEFORE INSERT ON sequences
		 BEGIN SELECT RAISE(ABORT, 'test: sequence rebuild fails'); END`)
	require.NoError(t, err)

	tasksPath := filepath.Join(app.mtixDir, "tasks.json")
	stale := []byte(`{"note": "stale"}` + "\n")
	require.NoError(t, os.WriteFile(tasksPath, stale, 0o644))

	cmd := newImportCmd()
	cmd.SetArgs([]string{path, "--mode", "replace"})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	err = cmd.Execute()
	require.Error(t, err)
	assert.ErrorIs(t, err, sqlite.ErrImportIncomplete)

	got, readErr := os.ReadFile(tasksPath)
	require.NoError(t, readErr)
	assert.NotEqual(t, stale, got, "an import that committed re-exports .mtix/tasks.json even though it failed")
}
