// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// CLI tests for `mtix sync repair --status` (MTIX-95.6). The fixture is
// ADR-006 scenario S7 as a pre-MTIX-95.2 pull left it: claim, push, done,
// then a pull that replayed the claim reverted the node to in_progress.

// setupRepairStatusRevert creates TEST-1 in the test app and leaves it
// reverted as S7 describes. The test (not the command) seeds the damage
// through the store.
func setupRepairStatusRevert(t *testing.T) {
	t.Helper()
	initTestApp(t)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	app.mtixDir = filepath.Join(cwd, ".mtix")
	require.NoError(t, runCreate("S7 node", "", "", 3, "", "", "", "", ""))
	ctx := testContext(t)
	require.NoError(t, app.store.ClaimNode(ctx, "TEST-1", "agent-a"))
	// A push marks the claim pushed.
	_, err = app.store.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed' WHERE sync_status = 'pending'`)
	require.NoError(t, err)
	require.NoError(t, app.store.TransitionStatus(ctx, "TEST-1", model.StatusDone, "finished", "agent-a"))
	// The pre-MTIX-95.2 pull replayed the claim with the v0.5.0-beta applyClaim statement.
	_, err = app.store.WriteDB().Exec(`UPDATE nodes SET status = ?, assignee = ?, agent_state = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`, "in_progress", "agent-a", "working", "2026-09-01T00:00:00Z", "TEST-1")
	require.NoError(t, err)
}

// testNodeStatus reads a node's status through the service layer.
func testNodeStatus(t *testing.T, id string) model.Status {
	t.Helper()
	n, err := app.nodeSvc.GetNode(testContext(t), id)
	require.NoError(t, err)
	return n.Status
}

// TestNewSyncCmd_RegistersRepair: `mtix sync repair` is a subcommand of
// `mtix sync` with the --status and --apply flags.
func TestNewSyncCmd_RegistersRepair(t *testing.T) {
	repair, _, err := newSyncCmd().Find([]string{"repair"})
	require.NoError(t, err)
	require.Equal(t, "repair", repair.Name())
	for _, flag := range []string{"status", "apply"} {
		f := repair.Flags().Lookup(flag)
		require.NotNil(t, f, "flag --%s", flag)
		require.Equal(t, "false", f.DefValue, "--%s is off by default", flag)
	}
}

// TestRunSyncRepair_WithoutStatus_ReturnsError: --status is the only repair,
// and it must be named.
func TestRunSyncRepair_WithoutStatus_ReturnsError(t *testing.T) {
	setupRepairStatusRevert(t)
	var out bytes.Buffer
	err := runSyncRepair(testContext(t), &out, syncRepairFlags{apply: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--status")
	require.Equal(t, model.StatusInProgress, testNodeStatus(t, "TEST-1"), "nothing is repaired")
}

// TestRunSyncRepair_DryRun_ListsTheReplayRevert: the default is a dry run
// that prints the one difference and changes nothing.
func TestRunSyncRepair_DryRun_ListsTheReplayRevert(t *testing.T) {
	setupRepairStatusRevert(t)
	var out bytes.Buffer

	require.NoError(t, runSyncRepair(testContext(t), &out, syncRepairFlags{status: true}))

	text := out.String()
	require.Contains(t, text, "DRY RUN")
	require.Contains(t, text, "1 node")
	require.Contains(t, text, "TEST-1")
	require.Contains(t, text, "status: in_progress -> done")
	require.Contains(t, text, "--apply")
	require.Equal(t, model.StatusInProgress, testNodeStatus(t, "TEST-1"))
}

// TestRunSyncRepair_JSON_ListsTheReplayRevert: --json prints the service's
// report.
func TestRunSyncRepair_JSON_ListsTheReplayRevert(t *testing.T) {
	setupRepairStatusRevert(t)
	app.jsonOutput = true
	var out bytes.Buffer

	require.NoError(t, runSyncRepair(testContext(t), &out, syncRepairFlags{status: true}))

	var report service.StatusRepairReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &report))
	require.False(t, report.Apply)
	require.Len(t, report.Differences, 1)
	require.Equal(t, "TEST-1", report.Differences[0].NodeID)
}

// TestSyncRepairCmd_Apply_RepairsBacksUpAndExports runs the cobra command
// with --status --apply: the node is done again, the backup is reported, and
// the normal auto-export rewrites .mtix/tasks.json.
func TestSyncRepairCmd_Apply_RepairsBacksUpAndExports(t *testing.T) {
	setupRepairStatusRevert(t)
	tasksPath := filepath.Join(app.mtixDir, "tasks.json")
	_ = os.Remove(tasksPath)
	cmd := newSyncRepairCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--status", "--apply"})

	require.NoError(t, cmd.ExecuteContext(testContext(t)))

	require.Equal(t, model.StatusDone, testNodeStatus(t, "TEST-1"))
	text := out.String()
	require.Contains(t, text, "Backup: "+filepath.Join(app.mtixDir, "data", "backups", "pre-repair-status-"))
	require.Contains(t, text, "Repaired 1 node")
	data, err := os.ReadFile(tasksPath)
	require.NoError(t, err, "apply re-exports tasks.json")
	require.Contains(t, string(data), `"status": "done"`)
}

// TestSyncRepairCmd_DryRun_DoesNotExport: a dry run writes nothing, so it does
// not export either.
func TestSyncRepairCmd_DryRun_DoesNotExport(t *testing.T) {
	setupRepairStatusRevert(t)
	tasksPath := filepath.Join(app.mtixDir, "tasks.json")
	_ = os.Remove(tasksPath)
	cmd := newSyncRepairCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--status"})

	require.NoError(t, cmd.ExecuteContext(testContext(t)))

	_, err := os.Stat(tasksPath)
	require.True(t, os.IsNotExist(err), "a dry run does not export")
}

// TestSyncRepairCmd_ApplyWithoutStatus_WritesAndExportsNothing: a refused
// --apply changes nothing, so the auto-export is skipped too.
func TestSyncRepairCmd_ApplyWithoutStatus_WritesAndExportsNothing(t *testing.T) {
	setupRepairStatusRevert(t)
	tasksPath := filepath.Join(app.mtixDir, "tasks.json")
	_ = os.Remove(tasksPath)
	cmd := newSyncRepairCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetArgs([]string{"--apply"})

	require.Error(t, cmd.ExecuteContext(testContext(t)))

	require.Equal(t, model.StatusInProgress, testNodeStatus(t, "TEST-1"))
	_, err := os.Stat(tasksPath)
	require.True(t, os.IsNotExist(err), "nothing was written, so nothing is exported")
}

// TestSyncRepairCmd_ReachesStoreOnlyThroughService guards the architecture
// rule: the command file neither imports a store package nor touches
// app.store; it calls the sync service.
func TestSyncRepairCmd_ReachesStoreOnlyThroughService(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "sync_repair.go", nil, 0)
	require.NoError(t, err)
	for _, imp := range file.Imports {
		path, unquoteErr := strconv.Unquote(imp.Path.Value)
		require.NoError(t, unquoteErr)
		require.False(t, strings.HasPrefix(path, "github.com/hyper-swe/mtix/internal/store"),
			"sync_repair.go imports %s", path)
	}
	usesService := false
	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		if x, isIdent := sel.X.(*ast.Ident); isIdent && x.Name == "app" {
			require.NotEqual(t, "store", sel.Sel.Name, "sync_repair.go must not use app.store")
			usesService = usesService || sel.Sel.Name == "syncSvc"
		}
		return true
	})
	require.True(t, usesService, "sync_repair.go reaches the store through app.syncSvc")
}
