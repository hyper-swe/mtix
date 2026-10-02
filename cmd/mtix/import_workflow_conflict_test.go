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
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// workflowBoard writes the local store's export, with edit applied,, as the board a teammate pushed, and returns its path. The
// checksum is recomputed over the edit (MTIX-95.31.13).
func workflowBoard(t *testing.T, edit func(data *sqlite.ExportData)) string {
	t.Helper()
	data, err := app.store.Export(t.Context(), "", "")
	require.NoError(t, err)
	edit(data)
	require.NoError(t, sqlite.RecomputeExportChecksum(data))
	raw, err := json.MarshalIndent(data, "", "  ")
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(path, raw, 0o600))
	return path
}

// TestRunImport_Merge_WorkflowConflict_RefusesAndWritesNothing verifies mtix
// import --mode merge refuses, writing nothing and taking no backup, in
// both directions: a teammate's claim over unchanged content, and a local
// reassignment a teammate's text edit would undo (MTIX-95.31.13).
func TestRunImport_Merge_WorkflowConflict_RefusesAndWritesNothing(t *testing.T) {
	tests := []struct {
		name  string
		local func(t *testing.T)
		edit  func(data *sqlite.ExportData)
	}{
		{"teammate claim, content unchanged", func(*testing.T) {}, func(data *sqlite.ExportData) {
			data.Nodes[0].Status, data.Nodes[0].Assignee = string(model.StatusInProgress), "bob"
		}},
		{"local reassign undone by a teammate's text edit", func(t *testing.T) {
			who := "carol"
			require.NoError(t, app.store.UpdateNode(t.Context(), "TEST-1", &store.NodeUpdate{Assignee: &who}))
		}, func(data *sqlite.ExportData) {
			data.Nodes[0].Title, data.Nodes[0].ContentHash = "Reworded by the teammate", "another-hash"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
			board := workflowBoard(t, tt.edit)
			tt.local(t)
			before, err := app.store.Export(t.Context(), "", "")
			require.NoError(t, err)

			err = runImport(board, importFlags{mode: "merge"})

			require.Error(t, err)
			after, err := app.store.Export(t.Context(), "", "")
			require.NoError(t, err)
			assert.Equal(t, before.Checksum, after.Checksum, "a refusal writes nothing")
			assert.Empty(t, preSyncBackups(t), "a refusal takes no backup")
		})
	}
}

// TestRunImport_Merge_WorkflowFlags_SettleTheConflict verifies each flag
// settles a conflicting task as the table says, and that the choice is
// recorded in the task's activity (MTIX-95.31.13).
func TestRunImport_Merge_WorkflowFlags_SettleTheConflict(t *testing.T) {
	tests := []struct {
		name       string
		flags      importFlags
		wantStatus model.Status
		wantWho    string
	}{
		{"prefer theirs", importFlags{prefer: "theirs"}, model.StatusInProgress, "bob"},
		{"prefer ours", importFlags{prefer: "ours"}, model.StatusOpen, ""},
		{"theirs list", importFlags{theirs: []string{"TEST-1"}}, model.StatusInProgress, "bob"},
		{"ours list", importFlags{ours: []string{"TEST-1"}}, model.StatusOpen, ""},
		{"ours list beats prefer theirs", importFlags{prefer: "theirs", ours: []string{"TEST-1"}}, model.StatusOpen, ""},
		{"theirs list with spaces", importFlags{theirs: []string{" TEST-1 "}}, model.StatusInProgress, "bob"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
			board := workflowBoard(t, func(data *sqlite.ExportData) {
				data.Nodes[0].Status, data.Nodes[0].Assignee = string(model.StatusInProgress), "bob"
			})
			tt.flags.mode = "merge"

			require.NoError(t, runImport(board, tt.flags))

			n, err := app.store.GetNode(t.Context(), "TEST-1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, n.Status)
			assert.Equal(t, tt.wantWho, n.Assignee)
			entries, err := app.store.GetActivity(t.Context(), "TEST-1", 50, 0)
			require.NoError(t, err)
			recorded := 0
			for _, e := range entries {
				if e.Author == "import" && e.Type == model.ActivityTypeSystem {
					recorded++
				}
			}
			assert.Equal(t, 1, recorded, "the choice is recorded in the task's activity")
			assert.Len(t, preSyncBackups(t), 1, "the applied merge backed up the database")
		})
	}
}

// TestRunImport_Merge_WorkflowFlags_Invalid_WriteNothing verifies a
// partially covering choice, an unknown --prefer value, a task that does
// not conflict and the flags on a replace import are all refused before any
// write.
func TestRunImport_Merge_WorkflowFlags_Invalid_WriteNothing(t *testing.T) {
	tests := []struct {
		name  string
		flags importFlags
	}{
		{"list does not cover the conflict", importFlags{mode: "merge", theirs: []string{"TEST-2"}}},
		{"unknown prefer value", importFlags{mode: "merge", prefer: "mine"}},
		{"flags on replace", importFlags{mode: "replace", prefer: "theirs"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("Local task", "", "epic", 3, "", "", "", "", ""))
			board := workflowBoard(t, func(data *sqlite.ExportData) { data.Nodes[0].Assignee = "bob" })
			before, err := app.store.Export(t.Context(), "", "")
			require.NoError(t, err)

			require.Error(t, runImport(board, tt.flags))

			after, err := app.store.Export(t.Context(), "", "")
			require.NoError(t, err)
			assert.Equal(t, before.Checksum, after.Checksum)
			assert.Empty(t, preSyncBackups(t))
		})
	}
}

// TestNewImportCmd_WorkflowFlags_AreRegistered verifies the command carries
// the three flags with their help.
func TestNewImportCmd_WorkflowFlags_AreRegistered(t *testing.T) {
	cmd := newImportCmd()
	for _, name := range []string{"prefer", "theirs", "ours"} {
		flag := cmd.Flags().Lookup(name)
		require.NotNil(t, flag, name)
		assert.Contains(t, flag.Usage, "Merge only")
	}
}
