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
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// writeOtherTaskImportFile writes an export whose TEST-1 is a teammate's
// task, a different task (another uid) than the local TEST-1, and returns
// its path.
func writeOtherTaskImportFile(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second).Add(-time.Hour) // created apart from the local TEST-1
	src, err := sqlite.New(filepath.Join(t.TempDir(), "src.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	require.NoError(t, src.CreateNode(ctx, &model.Node{
		ID: "TEST-1", Project: "TEST", Depth: 0, Seq: 1, Title: "Teammate task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "t1", UID: mustUID(t),
		CreatedAt: now, UpdatedAt: now,
	}))
	data, err := src.Export(ctx, "TEST", "test")
	require.NoError(t, err)
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

// readRemapFile returns the uid -> display path map of a remap file.
func readRemapFile(t *testing.T, path string) map[string]string {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	var remap map[string]string
	require.NoError(t, json.Unmarshal(raw, &remap))
	return remap
}

// TestRunImport_DifferentTaskUnderID_RenumbersOnlyWithConfirm verifies mtix
// import --mode merge of a board whose TEST-1 is a different task keeps the
// local TEST-1 (MTIX-95.31.4): without --confirm it writes nothing and the
// remap file already names the renumbering; with --confirm the local task
// moves to TEST-2 with its uid, the board's task takes TEST-1, and the
// remap file records the move.
func TestRunImport_DifferentTaskUnderID_RenumbersOnlyWithConfirm(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
	local, err := app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	path := writeOtherTaskImportFile(t)
	remapPath := filepath.Join(t.TempDir(), "remap.json")

	err = runImport(path, importFlags{mode: "merge", remapFile: remapPath})
	require.ErrorIs(t, err, sqlite.ErrImportConfirmationRequired)
	unchanged, err := app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, local.UID, unchanged.UID, "without --confirm nothing is written")
	assert.Equal(t, "TEST-2", readRemapFile(t, remapPath)[local.UID])

	require.NoError(t, runImport(path, importFlags{mode: "merge", confirm: true, remapFile: remapPath}))
	moved, err := app.store.ResolveDisplayPathByUID(t.Context(), local.UID)
	require.NoError(t, err)
	assert.Equal(t, "TEST-2", moved)
	theirs, err := app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, "Teammate task", theirs.Title)
	assert.Equal(t, map[string]string{local.UID: "TEST-2"}, readRemapFile(t, remapPath))
}

// preSyncBackups lists the pre-import backups of the test project.
func preSyncBackups(t *testing.T) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(app.mtixDir, "data", "backups", "pre-sync-*.db"))
	require.NoError(t, err)
	return matches
}

// TestRunImport_Merge_BacksUpBeforeItWrites verifies mtix import --mode
// merge takes the verified pre-import backup before it writes, and none
// when it writes nothing (the renumbering awaits --confirm).
func TestRunImport_Merge_BacksUpBeforeItWrites(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
	path := writeOtherTaskImportFile(t)

	require.ErrorIs(t, runImport(path, importFlags{mode: "merge"}), sqlite.ErrImportConfirmationRequired)
	assert.Empty(t, preSyncBackups(t), "an import that writes nothing takes no backup")

	require.NoError(t, runImport(path, importFlags{mode: "merge", confirm: true}))
	assert.Len(t, preSyncBackups(t), 1, "the merge backed up the database before it wrote")
}

// TestPrintSyncReport_PendingImportOrOtherUID_HeadlineSaysSo verifies mtix
// sync never prints "In sync" while an import of tasks.json is pending or a
// task under one id differs, and says which.
func TestPrintSyncReport_PendingImportOrOtherUID_HeadlineSaysSo(t *testing.T) {
	tests := []struct {
		name   string
		report service.SyncReport
		want   []string
	}{
		{"pending import", service.SyncReport{FileNodeCount: 3, DBNodeCount: 3,
			AutoImport: service.AutoImportState{Enabled: true, Setting: "true",
				LastRefusal: &service.AutoImportRefusal{Pending: true, Kind: "lossy", Reason: "r"}}},
			[]string{"OUT OF SYNC: tasks.json changed and has not been imported"}},
		{"another task under an id", service.SyncReport{FileNodeCount: 3, DBNodeCount: 3, DifferentUID: []string{"PROJ-3"},
			AutoImport: service.AutoImportState{Enabled: true, Setting: "true"}},
			[]string{"OUT OF SYNC", "Another uid in tasks.json (1):", "    - PROJ-3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := captureStdout(t, func() { printSyncReport(&tt.report) })
			assert.NotContains(t, out, "In sync")
			for _, w := range tt.want {
				assert.Contains(t, out, w)
			}
		})
	}
}

// TestRunImport_MergeChangesNothing_TakesNoBackup verifies a merge that
// would change nothing (the same file again) takes no backup, so repeated
// merges never rotate the automatic import's backups away.
func TestRunImport_MergeChangesNothing_TakesNoBackup(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
	path := writeOtherTaskImportFile(t)
	require.NoError(t, runImport(path, importFlags{mode: "merge", confirm: true}))
	require.Len(t, preSyncBackups(t), 1)

	require.NoError(t, runImport(path, importFlags{mode: "merge", confirm: true}))
	assert.Len(t, preSyncBackups(t), 1, "a merge that changes nothing takes no backup")
}

// TestRunImport_MergeMovesLocalTask_RemapFileRecordsTheMove verifies the
// remap file records a local task the merge moves to the id the file holds
// it under (another clone renumbered it).
func TestRunImport_MergeMovesLocalTask_RemapFileRecordsTheMove(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
	local, err := app.store.GetNode(t.Context(), "TEST-1")
	require.NoError(t, err)
	ctx := context.Background()
	src, err := sqlite.New(filepath.Join(t.TempDir(), "src.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = src.Close() })
	earlier := local.CreatedAt.Add(-time.Hour)
	require.NoError(t, src.CreateNode(ctx, &model.Node{
		ID: "TEST-1", Project: "TEST", Depth: 0, Seq: 1, Title: "Teammate task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "t1", UID: mustUID(t), CreatedAt: earlier, UpdatedAt: earlier,
	}))
	moved := *local
	moved.ID, moved.Seq = "TEST-2", 2
	require.NoError(t, src.CreateNode(ctx, &moved))
	data, err := src.Export(ctx, "TEST", "test")
	require.NoError(t, err)
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	remapPath := filepath.Join(t.TempDir(), "remap.json")

	require.NoError(t, runImport(path, importFlags{mode: "merge", remapFile: remapPath}), "a move needs no --confirm")
	assert.Equal(t, map[string]string{local.UID: "TEST-2"}, readRemapFile(t, remapPath))
}
