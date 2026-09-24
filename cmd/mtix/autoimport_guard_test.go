// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for the MTIX-95.31.2 wiring in the CLI: persistentPreRun honours
// sync.auto_sync from .mtix/config.yaml, prints an auto-import refusal once
// per command (the service prints it; the root command does not log it
// again), and mtix sync reports the auto-import state. Written red-first.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// pulledProject creates a project with config (the content of
// .mtix/config.yaml) whose store holds TEST-1 with two annotations, exports
// it, then writes the board a teammate would push: TEST-1 without its
// annotations, plus a new task TEST-2. The app is reset afterwards, as
// before the next mtix command.
func pulledProject(t *testing.T, config string) {
	t.Helper()
	saveAndResetApp(t)
	tmpDir := t.TempDir()
	mtixDir := filepath.Join(tmpDir, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "config.yaml"), []byte(config), 0o644))
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	require.NoError(t, os.Chdir(tmpDir))

	require.NoError(t, initApp(&cobra.Command{Use: "test"}, ""))
	ctx := context.Background()
	ts := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	require.NoError(t, app.store.CreateNode(ctx, &model.Node{
		ID: "TEST-1", Project: "TEST", Depth: 0, Seq: 1, Title: "Reviewed task",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeEpic, ContentHash: "h-1", CreatedAt: ts, UpdatedAt: ts,
	}))
	require.NoError(t, app.store.SetAnnotations(ctx, "TEST-1", []model.Annotation{
		{ID: "01J9CLIPULL000000000000001", Author: "reviewer", Text: "PASS", CreatedAt: ts},
		{ID: "01J9CLIPULL000000000000002", Author: "lead", Text: "receipt", CreatedAt: ts.Add(time.Minute)},
	}))
	require.NoError(t, app.syncSvc.AutoExport(ctx, mtixDir))

	board, err := app.store.Export(ctx, "", "")
	require.NoError(t, err)
	board.Nodes[0].Annotations = nil
	added := board.Nodes[0]
	added.ID, added.Seq, added.UID, added.Title, added.ContentHash = "TEST-2", 2, "", "Teammate task", "h-2"
	board.Nodes = append(board.Nodes, added)
	require.NoError(t, sqlite.RecomputeExportChecksum(board))
	raw, err := json.MarshalIndent(board, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "tasks.json"), raw, 0o644))

	require.NoError(t, app.store.Close())
	app = appContext{}
	resetCloseOnce()
	t.Cleanup(func() {
		if app.store != nil {
			_ = app.store.Close()
		}
	})
}

// runPreRun runs the root command's pre-run for a command named name, as
// the next mtix command after the pull would, and returns its stderr.
func runPreRun(t *testing.T, name string) string {
	t.Helper()
	cmd := &cobra.Command{Use: name}
	cmd.SetContext(testContext(t))
	return captureStderr(t, func() {
		require.NoError(t, persistentPreRun(cmd, nil, ""))
	})
}

// TestPersistentPreRun_AutoSyncFalse_DoesNotAutoImport verifies the root
// command wires sync.auto_sync: with it false in .mtix/config.yaml the
// pulled board is not imported (not even refused).
func TestPersistentPreRun_AutoSyncFalse_DoesNotAutoImport(t *testing.T) {
	pulledProject(t, "prefix: TEST\nsync:\n  auto_sync: false\n")
	stderr := runPreRun(t, "list")

	assert.NotContains(t, stderr, "auto-import of .mtix/tasks.json refused")
	_, err := app.store.GetNode(testContext(t), "TEST-2")
	assert.ErrorIs(t, err, model.ErrNotFound, "auto-import is off")
	assert.False(t, app.syncSvc.AutoImportEnabled())
}

// TestPersistentPreRun_LossyTasksJSON_RefusalPrintedOnce is the git-pull
// regression through the CLI: the next command (mtix show) refuses the
// board, keeps both annotations and prints the refusal exactly once, with no
// second "auto-import failed" log line.
func TestPersistentPreRun_LossyTasksJSON_RefusalPrintedOnce(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	stderr := runPreRun(t, "show")

	assert.Equal(t, 1, strings.Count(stderr, "auto-import of .mtix/tasks.json refused"), stderr)
	assert.NotContains(t, stderr, "auto-import failed")
	assert.Contains(t, stderr, "TEST-1: 2 annotations")
	node, err := app.store.GetNode(testContext(t), "TEST-1")
	require.NoError(t, err)
	assert.Len(t, node.Annotations, 2, "the local annotations survive the command")
}

// TestRunSync_AfterRefusal_ReportsAutoImportState verifies mtix sync prints
// the auto-import state and the last refusal, as text and as JSON.
func TestRunSync_AfterRefusal_ReportsAutoImportState(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	runPreRun(t, "show")
	cmd := &cobra.Command{Use: "sync"}
	cmd.SetContext(testContext(t))

	text := captureStdout(t, func() { require.NoError(t, runSync(cmd, false)) })
	assert.Contains(t, text, "Auto-import: enabled (sync.auto_sync: true)")
	assert.Contains(t, text, "Last auto-import refusal:")
	assert.Contains(t, text, "(pending)")
	assert.Contains(t, text, "TEST-1")

	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runSync(cmd, false)) })
	var report service.SyncReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	assert.True(t, report.AutoImport.Enabled)
	require.NotNil(t, report.AutoImport.LastRefusal)
	assert.True(t, report.AutoImport.LastRefusal.Pending)
}

// TestRunSync_AutoSyncFalse_ReportsDisabled verifies mtix sync says when
// auto-import is off.
func TestRunSync_AutoSyncFalse_ReportsDisabled(t *testing.T) {
	pulledProject(t, "prefix: TEST\nsync:\n  auto_sync: false\n")
	runPreRun(t, "list")
	cmd := &cobra.Command{Use: "sync"}
	cmd.SetContext(testContext(t))

	text := captureStdout(t, func() { require.NoError(t, runSync(cmd, false)) })
	assert.Contains(t, text, "Auto-import: disabled (sync.auto_sync: false)")
	assert.Contains(t, text, "Last auto-import refusal: none")
}

// TestRunSync_UnreadableAutoSync_ShowsRawValue verifies mtix sync shows a
// sync.auto_sync value that is neither true nor false as configured, and
// that the default (on) applies.
func TestRunSync_UnreadableAutoSync_ShowsRawValue(t *testing.T) {
	pulledProject(t, "prefix: TEST\nsync:\n  auto_sync: yes\n")
	stderr := runPreRun(t, "show")
	assert.Equal(t, 1, strings.Count(stderr, "neither true nor false"), stderr)
	cmd := &cobra.Command{Use: "sync"}
	cmd.SetContext(testContext(t))

	text := captureStdout(t, func() { require.NoError(t, runSync(cmd, false)) })
	assert.Contains(t, text, `Auto-import: enabled (sync.auto_sync: "yes" is neither true nor false`)
	app.jsonOutput = true
	out := captureStdout(t, func() { require.NoError(t, runSync(cmd, false)) })
	var report service.SyncReport
	require.NoError(t, json.Unmarshal([]byte(out), &report))
	assert.Equal(t, "yes", report.AutoImport.Setting)
	assert.NotEmpty(t, report.AutoImport.SettingError)
	assert.True(t, report.AutoImport.Enabled)
}

// TestPersistentPreRun_FreshCloneWithAutoSyncFalse_ImportsAndKeepsIDs is the
// fresh-clone case: .mtix/config.yaml is tracked, so sync.auto_sync false
// reaches a new clone, whose store is empty. The board must still be
// imported (nothing can be lost), so the first write neither overwrites the
// board nor reuses an id.
func TestPersistentPreRun_FreshCloneWithAutoSyncFalse_ImportsAndKeepsIDs(t *testing.T) {
	// A teammate's board with four tasks.
	saveAndResetApp(t)
	src := t.TempDir()
	srcStore, err := sqlite.New(filepath.Join(src, "data"), nil)
	require.NoError(t, err)
	ts := time.Date(2026, 9, 24, 8, 0, 0, 0, time.UTC)
	for seq := 1; seq <= 4; seq++ {
		id := "TEST-" + string(rune('0'+seq))
		require.NoError(t, srcStore.CreateNode(context.Background(), &model.Node{
			ID: id, Project: "TEST", Depth: 0, Seq: seq, Title: "Task " + id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeEpic, ContentHash: "h-" + id, CreatedAt: ts, UpdatedAt: ts,
		}))
	}
	board, err := srcStore.Export(context.Background(), "", "")
	require.NoError(t, err)
	require.NoError(t, srcStore.Close())
	raw, err := json.MarshalIndent(board, "", "  ")
	require.NoError(t, err)

	// The fresh clone: config.yaml and tasks.json, no .mtix/data.
	clone := t.TempDir()
	mtixDir := filepath.Join(clone, ".mtix")
	require.NoError(t, os.MkdirAll(mtixDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "config.yaml"),
		[]byte("prefix: TEST\nsync:\n  auto_sync: false\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(mtixDir, "tasks.json"), raw, 0o644))
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	require.NoError(t, os.Chdir(clone))
	t.Cleanup(func() {
		if app.store != nil {
			_ = app.store.Close()
		}
	})

	stderr := runPreRun(t, "create")
	assert.Contains(t, stderr, "mtix: imported the changed .mtix/tasks.json: nodes 4 added")
	require.False(t, app.syncSvc.AutoImportEnabled(), "the switch is off")
	for seq := 1; seq <= 4; seq++ {
		_, getErr := app.store.GetNode(testContext(t), "TEST-"+string(rune('0'+seq)))
		require.NoError(t, getErr, "the fresh clone holds the board")
	}

	require.NoError(t, runCreate("New task", "", "", 3, "", "", "", "", ""))
	require.NoError(t, app.syncSvc.AutoExport(testContext(t), mtixDir))
	after, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	require.NoError(t, err)
	var exported sqlite.ExportData
	require.NoError(t, json.Unmarshal(after, &exported))
	assert.Equal(t, 5, exported.NodeCount, "the first write keeps the four tasks and adds one")
	node, err := app.store.GetNode(testContext(t), "TEST-5")
	require.NoError(t, err, "the new task gets the next id, not TEST-1")
	assert.Equal(t, "New task", node.Title)
}

// TestPendingRefusal_WritesKeepTasksJSONUntilResolved verifies that while an
// auto-import refusal is pending, the auto-export after a write leaves the
// refused tasks.json alone and says so once, and that each deliberate
// resolution rewrites it: mtix import of that file, and mtix sync --fix.
func TestPendingRefusal_WritesKeepTasksJSONUntilResolved(t *testing.T) {
	for _, resolve := range []string{"import merge", "sync --fix"} {
		t.Run(resolve, func(t *testing.T) {
			pulledProject(t, "prefix: TEST\n")
			runPreRun(t, "create")
			tasksPath := filepath.Join(app.mtixDir, "tasks.json")
			pulled, err := os.ReadFile(tasksPath)
			require.NoError(t, err)

			var notices bytes.Buffer
			app.syncSvc.SetNoticeWriter(&notices)
			ts := time.Date(2026, 9, 24, 9, 0, 0, 0, time.UTC)
			require.NoError(t, app.store.CreateNode(testContext(t), &model.Node{
				ID: "TEST-9", Project: "TEST", Depth: 0, Seq: 9, Title: "Local task",
				Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
				NodeType: model.NodeTypeEpic, ContentHash: "h-9", CreatedAt: ts, UpdatedAt: ts,
			}))
			// A writing command exports twice (its RunE wrapper and PostRun).
			require.NoError(t, app.syncSvc.AutoExport(testContext(t), app.mtixDir))
			require.NoError(t, app.syncSvc.AutoExport(testContext(t), app.mtixDir))
			assert.Equal(t, 1, strings.Count(notices.String(), ".mtix/tasks.json was not rewritten"), notices.String())
			now, err := os.ReadFile(tasksPath)
			require.NoError(t, err)
			assert.Equal(t, string(pulled), string(now), "the teammate's board is not overwritten")

			cmd := &cobra.Command{Use: "sync"}
			cmd.SetContext(testContext(t))
			if resolve == "sync --fix" {
				captureStdout(t, func() { require.NoError(t, runSync(cmd, true)) })
			} else {
				captureStdout(t, func() { require.NoError(t, runImport(tasksPath, importFlags{mode: "merge"})) })
				require.NoError(t, app.syncSvc.AutoExport(testContext(t), app.mtixDir))
			}
			rewritten, err := os.ReadFile(tasksPath)
			require.NoError(t, err)
			assert.NotEqual(t, string(pulled), string(rewritten), "the resolution rewrites tasks.json")
			assert.Contains(t, string(rewritten), "Local task")
			text := captureStdout(t, func() { require.NoError(t, runSync(cmd, false)) })
			assert.Contains(t, text, "(resolved ")
		})
	}
}

// TestPersistentPreRun_SkipListMatchesCommandPath verifies the commands that
// never auto-import are matched by their full path, so a subcommand that
// shares a name (mtix sync init, mtix sync migrate) still auto-imports, and
// mtix sync itself does not (FR-15.2c, MTIX-95.31.2).
func TestPersistentPreRun_SkipListMatchesCommandPath(t *testing.T) {
	tests := []struct {
		path       []string
		wantImport bool
	}{
		{[]string{"sync", "init"}, true},
		{[]string{"sync", "migrate"}, true},
		{[]string{"sync"}, false},
		{[]string{"export"}, false},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.path, " "), func(t *testing.T) {
			pulledProject(t, "prefix: TEST\n")
			cmd, _, err := newRootCmd().Find(tt.path)
			require.NoError(t, err)
			require.Equal(t, tt.path[len(tt.path)-1], cmd.Name())
			cmd.SetContext(testContext(t))
			stderr := captureStderr(t, func() { require.NoError(t, persistentPreRun(cmd, nil, "")) })
			assert.Equal(t, tt.wantImport, strings.Contains(stderr, "auto-import of .mtix/tasks.json refused"), stderr)
		})
	}
}

// TestRunSync_FixWithUnparsableBoard_RewritesIt verifies the way out named
// for a board that fails its checks: mtix sync --fix rewrites it from the
// database even when it cannot be parsed (git conflict markers), which
// makes the comparison itself fail (MTIX-95.31.2).
func TestRunSync_FixWithUnparsableBoard_RewritesIt(t *testing.T) {
	pulledProject(t, "prefix: TEST\n")
	runPreRun(t, "list")
	tasksPath := filepath.Join(app.mtixDir, "tasks.json")
	require.NoError(t, os.WriteFile(tasksPath, []byte("<<<<<<< HEAD\n{}\n=======\n{}\n>>>>>>> theirs\n"), 0o644))
	cmd := &cobra.Command{Use: "sync"}
	cmd.SetContext(testContext(t))

	require.Error(t, runSync(cmd, false), "without --fix the comparison error is returned")
	stderr := captureStderr(t, func() {
		captureStdout(t, func() { require.NoError(t, runSync(cmd, true)) })
	})
	assert.Contains(t, stderr, "rewriting tasks.json from the database")
	rewritten, err := os.ReadFile(tasksPath)
	require.NoError(t, err)
	var board sqlite.ExportData
	require.NoError(t, json.Unmarshal(rewritten, &board), "tasks.json is a valid export again")
	assert.Equal(t, 1, board.NodeCount)
}
