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
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// CLI tests for `mtix sync repair --status` (MTIX-95.6). The fixture is
// ADR-006 scenario S7 as a pre-MTIX-95.2 pull left it: claim, push, done,
// then a pull that replayed the claim reverted the node to in_progress.

// revertNodeLikeS7 leaves id reverted as S7 describes. The test (not the
// command) seeds the damage through the store, stamped with a pull time
// after the winner as a real replay was.
func revertNodeLikeS7(t *testing.T, id string) {
	t.Helper()
	ctx := testContext(t)
	require.NoError(t, app.store.ClaimNode(ctx, id, "agent-a"))
	// A push marks the claim pushed.
	_, err := app.store.WriteDB().Exec(`UPDATE sync_events SET sync_status = 'pushed' WHERE sync_status = 'pending'`)
	require.NoError(t, err)
	require.NoError(t, app.store.TransitionStatus(ctx, id, model.StatusDone, "finished", "agent-a"))
	// The pre-MTIX-95.2 pull replayed the claim with the v0.5.0-beta applyClaim statement.
	_, err = app.store.WriteDB().Exec(`UPDATE nodes SET status = ?, assignee = ?, agent_state = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`, "in_progress", "agent-a", "working",
		time.Now().UTC().Add(time.Hour).Format(time.RFC3339), id)
	require.NoError(t, err)
}

// setupRepairStatusRevert creates TEST-1 in the test app and leaves it
// reverted as S7 describes.
func setupRepairStatusRevert(t *testing.T) {
	t.Helper()
	initTestApp(t)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	app.mtixDir = filepath.Join(cwd, ".mtix")
	require.NoError(t, runCreate("S7 node", "", "", 3, "", "", "", "", ""))
	revertNodeLikeS7(t, "TEST-1")
}

// addFlaggedNode creates TEST-2, claims it and sets it done without an event,
// as an import of a teammate's tasks.json does.
func addFlaggedNode(t *testing.T) {
	t.Helper()
	require.NoError(t, runCreate("imported node", "", "", 3, "", "", "", "", ""))
	require.NoError(t, app.store.ClaimNode(testContext(t), "TEST-2", "agent-a"))
	// A newer state that arrived without an event.
	_, err := app.store.WriteDB().Exec(`UPDATE nodes SET status = 'done', closed_at = ? WHERE id = 'TEST-2'`,
		time.Now().UTC().Format(time.RFC3339))
	require.NoError(t, err)
}

// removeTasksJSON deletes .mtix/tasks.json if it exists and returns its path.
func removeTasksJSON(t *testing.T) string {
	t.Helper()
	path := filepath.Join(app.mtixDir, "tasks.json")
	if err := os.Remove(path); err != nil {
		require.True(t, os.IsNotExist(err), "remove tasks.json: %v", err)
	}
	return path
}

// requireNoFile asserts that path does not exist.
func requireNoFile(t *testing.T, path, why string) {
	t.Helper()
	_, err := os.Stat(path)
	require.True(t, os.IsNotExist(err), why)
}

// testNodeStatus reads a node's status through the service layer.
func testNodeStatus(t *testing.T, id string) model.Status {
	t.Helper()
	n, err := app.nodeSvc.GetNode(testContext(t), id)
	require.NoError(t, err)
	return n.Status
}

// runRepairCmd runs `mtix sync repair` with args and returns its output.
func runRepairCmd(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newSyncRepairCmd()
	// As under the root command, which silences cobra's usage and error text.
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs(args)
	err := cmd.ExecuteContext(testContext(t))
	return out.String(), err
}

// TestNewSyncCmd_RegistersRepair: `mtix sync repair` is a subcommand of
// `mtix sync` with the --status, --apply and --force flags.
func TestNewSyncCmd_RegistersRepair(t *testing.T) {
	repair, _, err := newSyncCmd().Find([]string{"repair"})
	require.NoError(t, err)
	require.Equal(t, "repair", repair.Name())
	for _, flag := range []string{"status", "apply", "force"} {
		f := repair.Flags().Lookup(flag)
		require.NotNil(t, f, "flag --%s", flag)
		require.Equal(t, "false", f.DefValue, "--%s is off by default", flag)
		require.NotEmpty(t, f.Usage, "--%s is documented", flag)
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

// TestRunSyncRepair_DryRun_ListsReplayAndFlaggedNode: the default is a dry
// run that prints each difference with its winner, the older event it
// replays (if any) and the reason, marks the flagged node, and changes
// nothing.
func TestRunSyncRepair_DryRun_ListsReplayAndFlaggedNode(t *testing.T) {
	setupRepairStatusRevert(t)
	addFlaggedNode(t)
	var out bytes.Buffer

	require.NoError(t, runSyncRepair(testContext(t), &out, syncRepairFlags{status: true}))

	text := out.String()
	for _, want := range []string{
		"DRY RUN", "2 nodes", "1 flagged", "--apply", "--force",
		"TEST-1  replay: the stored state is the row of an older event",
		"status: in_progress -> done", "winner: transition_status", "(this machine)", "replayed event: ",
		"TEST-2  FLAGGED  not a replay; review", "winner: claim",
	} {
		require.Contains(t, text, want)
	}
	require.Equal(t, model.StatusInProgress, testNodeStatus(t, "TEST-1"))
	require.Equal(t, model.StatusDone, testNodeStatus(t, "TEST-2"))
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
	require.NotEmpty(t, report.Differences[0].WinnerWallClock)
	require.NotEmpty(t, report.Differences[0].Reason)
}

// TestSyncRepairCmd_Apply_RepairsBacksUpAndExports runs the cobra command
// with --status --apply: the replay is repaired, the flagged node is skipped
// and named, the backup is reported, and .mtix/tasks.json is re-exported.
func TestSyncRepairCmd_Apply_RepairsBacksUpAndExports(t *testing.T) {
	setupRepairStatusRevert(t)
	addFlaggedNode(t)
	tasksPath := removeTasksJSON(t)

	text, err := runRepairCmd(t, "--status", "--apply")

	require.NoError(t, err)
	require.Equal(t, model.StatusDone, testNodeStatus(t, "TEST-1"))
	require.Equal(t, model.StatusDone, testNodeStatus(t, "TEST-2"), "the flagged node is skipped")
	require.Contains(t, text, "Backup: "+filepath.Join(app.mtixDir, "data", "backups", "pre-repair-status-"))
	require.Contains(t, text, "Repaired 1 node")
	require.Contains(t, text, "Skipped 1 flagged node")
	data, err := os.ReadFile(tasksPath)
	require.NoError(t, err, "apply re-exports tasks.json")
	require.Contains(t, string(data), `"status": "done"`)
}

// TestSyncRepairCmd_ApplyForce_RepairsFlaggedNode: --apply --force repairs a
// flagged node too.
func TestSyncRepairCmd_ApplyForce_RepairsFlaggedNode(t *testing.T) {
	setupRepairStatusRevert(t)
	addFlaggedNode(t)

	_, err := runRepairCmd(t, "--status", "--apply", "--force")

	require.NoError(t, err)
	require.Equal(t, model.StatusDone, testNodeStatus(t, "TEST-1"))
	require.Equal(t, model.StatusInProgress, testNodeStatus(t, "TEST-2"))
}

// TestSyncRepairCmd_ForceWithoutApply_WritesNothing: --force needs --apply.
func TestSyncRepairCmd_ForceWithoutApply_WritesNothing(t *testing.T) {
	setupRepairStatusRevert(t)
	addFlaggedNode(t)
	tasksPath := removeTasksJSON(t)

	_, err := runRepairCmd(t, "--status", "--force")

	require.Error(t, err)
	require.Contains(t, err.Error(), "--apply")
	require.Equal(t, model.StatusDone, testNodeStatus(t, "TEST-2"))
	requireNoFile(t, tasksPath, "nothing was written, so nothing is exported")
}

// TestSyncRepairCmd_DryRun_DoesNotExport: a dry run writes nothing, so it does
// not export either.
func TestSyncRepairCmd_DryRun_DoesNotExport(t *testing.T) {
	setupRepairStatusRevert(t)
	tasksPath := removeTasksJSON(t)

	_, err := runRepairCmd(t, "--status")

	require.NoError(t, err)
	requireNoFile(t, tasksPath, "a dry run does not export")
}

// TestSyncRepairCmd_ApplyWithoutStatus_WritesAndExportsNothing: a refused
// --apply changes nothing, so the auto-export is skipped too.
func TestSyncRepairCmd_ApplyWithoutStatus_WritesAndExportsNothing(t *testing.T) {
	setupRepairStatusRevert(t)
	tasksPath := removeTasksJSON(t)

	_, err := runRepairCmd(t, "--apply")

	require.Error(t, err)
	require.Equal(t, model.StatusInProgress, testNodeStatus(t, "TEST-1"))
	requireNoFile(t, tasksPath, "nothing was written, so nothing is exported")
}

// TestSyncRepairCmd_ApplyWithNothingToRepair_LeavesTasksJSON: an --apply that
// repairs nothing writes nothing, so .mtix/tasks.json is not rewritten.
func TestSyncRepairCmd_ApplyWithNothingToRepair_LeavesTasksJSON(t *testing.T) {
	initTestApp(t)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	app.mtixDir = filepath.Join(cwd, ".mtix")
	require.NoError(t, runCreate("healthy node", "", "", 3, "", "", "", "", ""))
	require.NoError(t, app.syncSvc.AutoExport(testContext(t), app.mtixDir))
	tasksPath := filepath.Join(app.mtixDir, "tasks.json")
	before, err := os.ReadFile(tasksPath)
	require.NoError(t, err)
	time.Sleep(1100 * time.Millisecond) // an export would carry a later exported_at

	text, err := runRepairCmd(t, "--status", "--apply")

	require.NoError(t, err)
	require.Contains(t, text, "No differences")
	after, err := os.ReadFile(tasksPath)
	require.NoError(t, err)
	require.Equal(t, string(before), string(after), "tasks.json is not rewritten")
}

// TestSyncRepairCmd_ApplyFailsPartWay_ExportsAndReturnsError: when a repair
// fails part-way, the command reports the error, and the nodes already
// repaired are exported.
func TestSyncRepairCmd_ApplyFailsPartWay_ExportsAndReturnsError(t *testing.T) {
	setupRepairStatusRevert(t)
	require.NoError(t, runCreate("second node", "", "", 3, "", "", "", "", ""))
	revertNodeLikeS7(t, "TEST-2")
	// Any write to TEST-2 now fails.
	_, err := app.store.WriteDB().Exec(`CREATE TRIGGER fail_test2 BEFORE UPDATE ON nodes
		WHEN NEW.id = 'TEST-2' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	require.NoError(t, err)
	tasksPath := removeTasksJSON(t)

	text, err := runRepairCmd(t, "--status", "--apply")

	require.Error(t, err)
	require.Contains(t, text, "Repaired 1 node")
	require.Equal(t, model.StatusDone, testNodeStatus(t, "TEST-1"))
	data, readErr := os.ReadFile(tasksPath)
	require.NoError(t, readErr, "the nodes already repaired are exported")
	require.Contains(t, string(data), `"status": "done"`)
}

// TestSyncRepairCmd_ApplyFailsPartWay_JSON_PrintsTheReport: with --json, a
// repair that fails part-way still prints the report as JSON, so an agent
// parsing stdout sees what was repaired, and the command returns the error.
func TestSyncRepairCmd_ApplyFailsPartWay_JSON_PrintsTheReport(t *testing.T) {
	setupRepairStatusRevert(t)
	require.NoError(t, runCreate("second node", "", "", 3, "", "", "", "", ""))
	revertNodeLikeS7(t, "TEST-2")
	// Any write to TEST-2 now fails.
	_, err := app.store.WriteDB().Exec(`CREATE TRIGGER fail_test2 BEFORE UPDATE ON nodes
		WHEN NEW.id = 'TEST-2' BEGIN SELECT RAISE(ABORT, 'injected failure'); END`)
	require.NoError(t, err)
	app.jsonOutput = true

	text, err := runRepairCmd(t, "--status", "--apply")

	require.Error(t, err)
	var report service.StatusRepairReport
	require.NoError(t, json.Unmarshal([]byte(text), &report), "stdout is the JSON report: %s", text)
	require.Len(t, report.Repaired, 1)
	require.Equal(t, "TEST-1", report.Repaired[0].NodeID)
}

// TestSyncRepairCmd_PullFirstReminder_OnlyOnADryRunThatListsDifferences
// (MTIX-95.35): a dry run that lists differences ends with a one-line
// reminder to run mtix sync pull first, in text as its last line and in
// --json as the "reminder" field. A repair made on a stale local log can
// revert a teammate's newer, unpulled change on every machine. There is no
// reminder when nothing differs, and none with --apply.
func TestSyncRepairCmd_PullFirstReminder_OnlyOnADryRunThatListsDifferences(t *testing.T) {
	tests := []struct {
		name     string
		damaged  bool
		json     bool
		args     []string
		reminder bool
	}{
		{"dry run listing differences, text", true, false, []string{"--status"}, true},
		{"dry run listing differences, json", true, true, []string{"--status"}, true},
		{"dry run with nothing to list, text", false, false, []string{"--status"}, false},
		{"dry run with nothing to list, json", false, true, []string{"--status"}, false},
		{"apply, text", true, false, []string{"--status", "--apply"}, false},
		{"apply, json", true, true, []string{"--status", "--apply"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.damaged {
				setupRepairStatusRevert(t)
			} else {
				initTestApp(t)
				cwd, err := os.Getwd()
				require.NoError(t, err)
				app.mtixDir = filepath.Join(cwd, ".mtix")
				require.NoError(t, runCreate("healthy node", "", "", 3, "", "", "", "", ""))
			}
			app.jsonOutput = tt.json

			text, err := runRepairCmd(t, tt.args...)

			require.NoError(t, err)
			if tt.json {
				var report map[string]any
				require.NoError(t, json.Unmarshal([]byte(text), &report), text)
				reminder, present := report["reminder"]
				require.Equal(t, tt.reminder, present, "the reminder field is present only on such a dry run")
				if tt.reminder {
					require.Contains(t, reminder, "mtix sync pull")
				}
				return
			}
			lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
			last := lines[len(lines)-1]
			if tt.reminder {
				require.Contains(t, last, "mtix sync pull", "the dry run ends with the pull-first reminder")
				require.Contains(t, last, "teammate", "the reminder says why")
			} else {
				require.NotContains(t, text, "mtix sync pull")
			}
		})
	}
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
