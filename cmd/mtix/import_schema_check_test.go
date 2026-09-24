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
