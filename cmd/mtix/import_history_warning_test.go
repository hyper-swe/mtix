// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// writeExportBy writes the store's export stamped with the given last-writer
// version and returns its path.
func writeExportBy(t *testing.T, writer string) string {
	t.Helper()
	data, err := app.store.Export(t.Context(), "TEST", writer)
	require.NoError(t, err)
	data.MtixVersion = writer
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "export.json")
	require.NoError(t, os.WriteFile(path, raw, 0o644))
	return path
}

func withBinaryVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

// TestRunImport_OlderWriter_WarnsInTextAndJSON pins the older-writer warning
// of mtix import: a stderr line in text mode, a warnings entry in the --json
// result, and nothing for a same or newer writer (MTIX-95.31.19.9).
func TestRunImport_OlderWriter_WarnsInTextAndJSON(t *testing.T) {
	const warn = "history may be incomplete: board last written by mtix 0.5.4; upgrade mtix on that machine"
	tests := []struct {
		name, writer string
		json, want   bool
	}{
		{"text older", "0.5.4", false, true},
		{"json older", "0.5.4", true, true},
		{"text same", "0.6.0", false, false},
		{"json newer", "0.7.0", true, false},
	}
	for _, tt := range tests {
		{ // replace needs a typed confirmation; both modes share reportImportResult
			t.Run(tt.name, func(t *testing.T) {
				initTestApp(t)
				withBinaryVersion(t, "0.6.0")
				enableHistoryExport(t)
				require.NoError(t, runCreate("Round trip", "", "", 3, "", "", "", "", ""))
				path := writeExportBy(t, tt.writer)
				app.jsonOutput = tt.json
				var out string
				errOut := captureStderr(t, func() {
					out = captureStdout(t, func() {
						require.NoError(t, runImport(path, importFlags{mode: "merge", force: true}))
					})
				})
				if !tt.json {
					assert.Equal(t, tt.want, strings.Contains(errOut, warn), errOut)
					return
				}
				var res struct {
					Warnings []string `json:"warnings"`
				}
				require.NoError(t, json.Unmarshal([]byte(out), &res), out)
				if tt.want {
					assert.Contains(t, res.Warnings, warn)
				} else {
					assert.Empty(t, res.Warnings)
				}
			})
		}
	}
}

func TestRunImport_OlderWriter_SilentWhileHistoryExportIsOff(t *testing.T) {
	initTestApp(t)
	withBinaryVersion(t, "0.6.0")
	require.NoError(t, runCreate("Round trip", "", "", 3, "", "", "", "", ""))
	path := writeExportBy(t, "0.5.4")
	errOut := captureStderr(t, func() {
		_ = captureStdout(t, func() { require.NoError(t, runImport(path, importFlags{mode: "merge", force: true})) })
	})
	assert.NotContains(t, errOut, "history may be incomplete")
}

// An import that applied but whose index rebuild failed still reports its warnings.
func TestRunImport_AppliedButIncomplete_StillReportsWarnings(t *testing.T) {
	initTestApp(t)
	withBinaryVersion(t, "0.6.0")
	enableHistoryExport(t)
	require.NoError(t, runCreate("Round trip", "", "", 3, "", "", "", "", ""))
	path := writeExportBy(t, "0.5.4")
	_, err := app.store.WriteDB().ExecContext(t.Context(),
		`CREATE TRIGGER test_fail_sequence_rebuild BEFORE INSERT ON sequences
		 BEGIN SELECT RAISE(ABORT, 'test: sequence rebuild fails'); END`)
	require.NoError(t, err)
	var runErr error
	errOut := captureStderr(t, func() {
		_ = captureStdout(t, func() { runErr = runImport(path, importFlags{mode: "merge", force: true}) })
	})
	require.ErrorIs(t, runErr, sqlite.ErrImportIncomplete)
	assert.Contains(t, errOut, "history may be incomplete: board last written by mtix 0.5.4")
}

func enableHistoryExport(t *testing.T) {
	t.Helper()
	_, err := app.configSvc.Set("history.export", "true")
	require.NoError(t, err)
}
