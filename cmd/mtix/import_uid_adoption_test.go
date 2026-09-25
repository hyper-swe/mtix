// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// writeUpgradedTaskImportFile turns the local TEST-1 into a task created two
// hours ago, so its uid counts as one assigned at upgrade, and writes an
// export whose TEST-1 is the same id under another uid, with the title
// title. It returns the file's path, the local uid and the file's uid.
func writeUpgradedTaskImportFile(t *testing.T, title string) (path, localUID, fileUID string) {
	t.Helper()
	ctx := context.Background()
	created := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	_, err := app.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET created_at = ?, updated_at = ? WHERE id = 'TEST-1'`, created, created)
	require.NoError(t, err)
	data, err := app.store.Export(ctx, "TEST", "test")
	require.NoError(t, err)
	localUID, fileUID = data.Nodes[0].UID, mustUID(t)
	data.Nodes[0].UID, data.Nodes[0].Title, data.Nodes[0].ContentHash = fileUID, title, "h-"+title
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path = filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path, localUID, fileUID
}

// TestRunImport_MergeAdoptsUID_ListedInEveryOutput verifies mtix import
// --mode merge lists the uid it adopts (MTIX-95.31.6): the report on stderr
// names the id, both uids and both titles, the --json result carries it as
// uid_adoptions, and the remap file maps the local uid to the id.
func TestRunImport_MergeAdoptsUID_ListedInEveryOutput(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
	path, localUID, fileUID := writeUpgradedTaskImportFile(t, "Teammate task")
	remapPath := filepath.Join(t.TempDir(), "remap.json")
	app.jsonOutput = true

	var stdout string
	var importErr error
	stderr := captureStderr(t, func() {
		stdout = captureStdout(t, func() {
			importErr = runImport(path, importFlags{mode: "merge", remapFile: remapPath})
		})
	})
	require.NoError(t, importErr)

	assert.Contains(t, stderr, fmt.Sprintf("    - TEST-1 local uid=%s -> file uid=%s (local %q, file %q)\n",
		localUID, fileUID, "Local task", "Teammate task"))
	var result struct {
		UIDAdoptions []sqlite.ImportUIDAdoption `json:"uid_adoptions"`
	}
	require.NoError(t, json.Unmarshal([]byte(stdout), &result), stdout)
	assert.Equal(t, []sqlite.ImportUIDAdoption{{ID: "TEST-1", LocalUID: localUID, FileUID: fileUID,
		LocalTitle: "Local task", FileTitle: "Teammate task"}}, result.UIDAdoptions)
	assert.Equal(t, map[string]string{localUID: "TEST-1"}, readRemapFile(t, remapPath))
	node, err := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, fileUID, node.UID)
}

// TestRunImport_MergeAdoptsNothing_JSONAndRemapFileUnchanged verifies an
// import that adopts no uid prints no uid_adoptions key in --json and
// writes no remap file.
func TestRunImport_MergeAdoptsNothing_JSONAndRemapFileUnchanged(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
	data, err := app.store.Export(context.Background(), "TEST", "test")
	require.NoError(t, err)
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	remapPath := filepath.Join(t.TempDir(), "remap.json")
	app.jsonOutput = true

	var importErr error
	stdout := captureStdout(t, func() {
		importErr = runImport(path, importFlags{mode: "merge", remapFile: remapPath})
	})
	require.NoError(t, importErr)
	assert.NotContains(t, stdout, "uid_adoptions")
	_, statErr := os.Stat(remapPath)
	assert.ErrorIs(t, statErr, os.ErrNotExist)
}

// TestWriteRemapFile_UIDMintedByTheImport_OnlyOnceApplied verifies a remap
// file leaves out a uid the import minted for a local task that had none
// while the import is not applied (the confirmed run mints another), and
// holds it once the import is applied (MTIX-95.31.9).
func TestWriteRemapFile_UIDMintedByTheImport_OnlyOnceApplied(t *testing.T) {
	report := &sqlite.ImportReconcileReport{LocalRenumbers: []sqlite.ImportRemapEntry{
		{UID: "minted-uid", OldPath: "TEST-1", NewPath: "TEST-3", NewUID: true},
		{UID: "held-uid", OldPath: "TEST-2", NewPath: "TEST-4"},
	}}
	tests := []struct {
		name    string
		applied bool
		want    map[string]string
	}{
		{"not applied", false, map[string]string{"held-uid": "TEST-4"}},
		{"applied", true, map[string]string{"held-uid": "TEST-4", "minted-uid": "TEST-3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "remap.json")
			report.Applied = tt.applied
			require.NoError(t, writeRemapFile(path, report))
			assert.Equal(t, tt.want, readRemapFile(t, path))
		})
	}
}
