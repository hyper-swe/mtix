// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-2.3.2.2 (FR-7.8): the import report and the remap file are written by
// streaming a ReportSource, so a plan read from the staged export prints and
// writes exactly what the in-memory report does.
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// renumberingBoard is the local store's export with its first task replaced by a
// different task under the same id, so a merge must renumber the local one.
func renumberingBoard(t *testing.T) *sqlite.ExportData {
	t.Helper()
	data, err := app.store.Export(t.Context(), "", "")
	require.NoError(t, err)
	data.Nodes[0].UID = "01900000-0000-7000-8000-00000000beef"
	data.Nodes[0].Title = "Teammate task"
	return data
}

// printed returns what writeImportReport writes to stderr and to the remap file.
func printed(t *testing.T, src sqlite.ReportSource) (string, string) {
	t.Helper()
	remap := filepath.Join(t.TempDir(), "remap.json")
	var err error
	stderr := captureStderr(t, func() { err = writeImportReport(src, remap) })
	require.NoError(t, err)
	body, readErr := os.ReadFile(remap)
	require.NoError(t, readErr)
	return stderr, string(body)
}

func TestWriteImportReport_StagedPlanPrintsWhatTheInMemoryReportPrints(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "", 3, "", "", "", "", ""))
	opts := sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge}
	raw, err := json.Marshal(renumberingBoard(t))
	require.NoError(t, err)
	st, err := sqlite.StageExport(t.Context(), bytes.NewReader(raw), t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()

	plan, planErr := app.store.PlanImportStaged(t.Context(), st, opts)
	report, _, reconcileErr := app.store.ImportReconcile(t.Context(), renumberingBoard(t), opts)
	require.ErrorIs(t, planErr, sqlite.ErrImportConfirmationRequired)
	require.ErrorIs(t, reconcileErr, sqlite.ErrImportConfirmationRequired)

	wantErr, wantRemap := printed(t, report)
	gotErr, gotRemap := printed(t, plan)
	assert.Contains(t, gotErr, "local tasks renumbered")
	assert.Equal(t, wantErr, gotErr)
	assert.Equal(t, wantRemap, gotRemap)
	var remap map[string]string
	require.NoError(t, json.Unmarshal([]byte(gotRemap), &remap))
	assert.Len(t, remap, 1)
}

// The remap file of a plan with nothing to remap is not written, and one that
// cannot be written is an error that names the file.
func TestWriteRemapFile_PlanWithoutRemapWritesNothing(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "", 3, "", "", "", "", ""))
	data, err := app.store.Export(t.Context(), "", "")
	require.NoError(t, err)
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	st, err := sqlite.StageExport(t.Context(), bytes.NewReader(raw), t.TempDir())
	require.NoError(t, err)
	defer func() { require.NoError(t, st.Close()) }()
	plan, err := app.store.PlanImportStaged(t.Context(), st, sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err)

	path := filepath.Join(t.TempDir(), "remap.json")
	require.NoError(t, writeRemapFile(path, plan))
	_, statErr := os.Stat(path)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
	assert.NoError(t, writeRemapFile("", plan))
}
