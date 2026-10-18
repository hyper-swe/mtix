// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// exportSchemaRow is one doctor JSON row as the export schema tests read it.
type exportSchemaRow struct {
	ID, Status, Detail, Fix string
	Data                    map[string]any
}

// executeCreate runs mtix create through Cobra so the automatic export runs.
func executeCreate(t *testing.T, title string) {
	t.Helper()
	cmd := newCreateCmd()
	cmd.SetArgs([]string{title})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())
}

// boardSchemaVersion returns the schema_version .mtix/tasks.json declares.
func boardSchemaVersion(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(app.mtixDir, "tasks.json"))
	require.NoError(t, err)
	var head struct {
		SchemaVersion string `json:"schema_version"`
	}
	require.NoError(t, json.Unmarshal(raw, &head))
	return head.SchemaVersion
}

// setBoardSchemaVersion rewrites the schema_version .mtix/tasks.json
// declares and records the file's hash as mtix's last write, standing in for
// a board an older mtix exported and the repository committed.
func setBoardSchemaVersion(t *testing.T, version string) {
	t.Helper()
	path := filepath.Join(app.mtixDir, "tasks.json")
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var board map[string]any
	require.NoError(t, json.Unmarshal(raw, &board))
	board["schema_version"] = version
	out, err := json.MarshalIndent(board, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, out, 0o600))
	hash := fmt.Sprintf("%x", sha256.Sum256(out))
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "data", "sync.sha256"), []byte(hash), 0o600))
}

// TestAutoExport_OlderSchemaBoard_RewrittenInCurrentSchema reproduces
// MTIX-168: one plain write rewrites a board that declares an older schema
// in this binary's export schema, with sync.auto_sync on or off.
func TestAutoExport_OlderSchemaBoard_RewrittenInCurrentSchema(t *testing.T) {
	for _, autoSync := range []string{"true", "false"} {
		t.Run("auto_sync_"+autoSync, func(t *testing.T) {
			initTestApp(t)
			executeCreate(t, "first")
			_, err := app.configSvc.Set("sync.auto_sync", autoSync)
			require.NoError(t, err)
			setBoardSchemaVersion(t, "1.0.0")

			executeCreate(t, "second")

			assert.Equal(t, sqlite.SchemaVersionV1, boardSchemaVersion(t))
		})
	}
}

// runExportSchemaDoctor runs doctor and returns its output and the
// export.schema JSON row, if any.
func runExportSchemaDoctor(t *testing.T, jsonOutput bool) (string, *exportSchemaRow) {
	t.Helper()
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, runDoctor(t.Context(), &out, svc, doctorOutputOptions{JSON: jsonOutput}))
	if !jsonOutput {
		return out.String(), nil
	}
	var rows []exportSchemaRow
	require.NoError(t, json.Unmarshal(out.Bytes(), &rows), out.String())
	for i := range rows {
		if rows[i].ID == "export.schema" {
			return out.String(), &rows[i]
		}
	}
	return out.String(), nil
}

// TestDoctor_ExportSchemaCheck_MismatchWarnsWithFix verifies doctor warns,
// in text and JSON, when tasks.json declares another schema (MTIX-168).
func TestDoctor_ExportSchemaCheck_MismatchWarnsWithFix(t *testing.T) {
	initTestApp(t)
	executeCreate(t, "first")
	setBoardSchemaVersion(t, "1.0.0")

	text, _ := runExportSchemaDoctor(t, false)
	assert.Contains(t, text, "[WARN] export.schema")
	assert.Contains(t, text, "fix: ")
	assert.Contains(t, text, "mtix sync --fix")

	_, row := runExportSchemaDoctor(t, true)
	require.NotNil(t, row)
	assert.Equal(t, "warn", row.Status)
	assert.Contains(t, row.Detail, "1.0.0")
	assert.Contains(t, row.Detail, sqlite.SchemaVersionV1)
	assert.Contains(t, row.Fix, "mtix sync --fix")
	assert.Contains(t, row.Fix, "sync.auto_sync")
	assert.Nil(t, row.Data, "the row keeps exactly id, status, detail and fix")
}

// TestDoctor_ExportSchemaCheck_MatchPasses verifies doctor passes when
// tasks.json declares this binary's export schema (MTIX-168).
func TestDoctor_ExportSchemaCheck_MatchPasses(t *testing.T) {
	initTestApp(t)
	executeCreate(t, "first")

	text, _ := runExportSchemaDoctor(t, false)
	assert.Contains(t, text, "[PASS] export.schema")

	_, row := runExportSchemaDoctor(t, true)
	require.NotNil(t, row)
	assert.Equal(t, "pass", row.Status)
	assert.Empty(t, row.Fix)
}

// TestDoctor_ExportSchemaCheck_SyncFixUpgrades_ThenPasses verifies the
// printed fix rewrites the board in this binary's schema (MTIX-168).
func TestDoctor_ExportSchemaCheck_SyncFixUpgrades_ThenPasses(t *testing.T) {
	initTestApp(t)
	executeCreate(t, "first")
	setBoardSchemaVersion(t, "1.0.0")

	cmd := newSyncCmd()
	cmd.SetArgs([]string{"--fix"})
	cmd.SilenceUsage = true
	require.NoError(t, cmd.Execute())

	assert.Equal(t, sqlite.SchemaVersionV1, boardSchemaVersion(t))
	_, row := runExportSchemaDoctor(t, true)
	require.NotNil(t, row)
	assert.Equal(t, "pass", row.Status)
}

// TestDoctor_ExportSchemaCheck_HistoryExportExpectsHistorySchema verifies
// the expected schema follows history.export (MTIX-168).
func TestDoctor_ExportSchemaCheck_HistoryExportExpectsHistorySchema(t *testing.T) {
	initTestApp(t)
	executeCreate(t, "first")
	_, err := app.configSvc.Set("history.export", "true")
	require.NoError(t, err)

	_, row := runExportSchemaDoctor(t, true)

	require.NotNil(t, row)
	assert.Equal(t, "warn", row.Status)
	assert.Contains(t, row.Detail, "this binary writes schema "+sqlite.SchemaVersionHistory)
}

// TestDoctor_ExportSchemaCheck_NoBoardFile_NoRow verifies doctor adds no row
// when there is no tasks.json to compare (MTIX-168).
func TestDoctor_ExportSchemaCheck_NoBoardFile_NoRow(t *testing.T) {
	initTestApp(t)
	_ = os.Remove(filepath.Join(app.mtixDir, "tasks.json"))

	_, row := runExportSchemaDoctor(t, true)

	assert.Nil(t, row)
}

// TestDoctor_ExportSchemaCheck_UnreadableBoard_WarnsWithoutRewrite verifies a
// board file without valid JSON is reported, not rewritten (MTIX-168).
func TestDoctor_ExportSchemaCheck_UnreadableBoard_WarnsWithoutRewrite(t *testing.T) {
	initTestApp(t)
	path := filepath.Join(app.mtixDir, "tasks.json")
	require.NoError(t, os.WriteFile(path, []byte("not json"), 0o600))

	_, row := runExportSchemaDoctor(t, true)

	require.NotNil(t, row)
	assert.Equal(t, "warn", row.Status)
	assert.Contains(t, row.Detail, "could not read")
	assert.NotContains(t, row.Fix, "mtix sync --fix")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "not json", string(data))
}
