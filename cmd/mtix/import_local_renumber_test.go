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
)

// writeOtherTaskImportFile writes an export whose TEST-1 is a teammate's
// task, a different task (another uid) than the local TEST-1, and returns
// its path.
func writeOtherTaskImportFile(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
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
