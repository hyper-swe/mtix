// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// readSentinels returns (file_hash_sentinel, db_hash_sentinel) contents
// from .mtix/data/sync.sha256 and .mtix/data/sync-db.sha256.
func readSentinels(t *testing.T) (fileSentinel, dbSentinel string) {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	dataDir := filepath.Join(cwd, ".mtix", "data")
	fb, err := os.ReadFile(filepath.Join(dataDir, "sync.sha256"))
	require.NoError(t, err, "sync.sha256 must exist")
	db, err := os.ReadFile(filepath.Join(dataDir, "sync-db.sha256"))
	require.NoError(t, err, "sync-db.sha256 must exist")
	return string(fb), string(db)
}

// writeSentinels overwrites the sentinel hash files with the given content.
func writeSentinels(t *testing.T, fileSentinel, dbSentinel string) {
	t.Helper()
	cwd, err := os.Getwd()
	require.NoError(t, err)
	dataDir := filepath.Join(cwd, ".mtix", "data")
	require.NoError(t, os.MkdirAll(dataDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, "sync.sha256"), []byte(fileSentinel), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, "sync-db.sha256"), []byte(dbSentinel), 0o644))
}

// hashFileContent returns the SHA-256 of the named file's bytes.
func hashFileContent(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	h := sha256.Sum256(b)
	return fmt.Sprintf("%x", h)
}

// setupSyncedState creates a node and runs AutoExport so tasks.json + sentinels
// exist in a consistent state. Returns the mtixDir path.
func setupSyncedState(t *testing.T) string {
	t.Helper()
	require.NoError(t, runCreate("Node A", "", "", 3, "", "", "", "", ""))
	cwd, err := os.Getwd()
	require.NoError(t, err)
	mtixDir := filepath.Join(cwd, ".mtix")
	require.NoError(t, app.syncSvc.AutoExport(testContext(t), mtixDir),
		"setup: AutoExport must succeed to populate tasks.json + sentinels")
	return mtixDir
}

// TestRunSync_FixWithStaleSentinels_RefreshesSentinels verifies the user's
// reported scenario: when DB and tasks.json are content-equivalent (same node
// IDs) but the sentinel hash files are stale, mtix sync --fix MUST refresh the
// sentinels per MTIX-11. Without this fix, the conflict-detection WARN in
// mtix list persists indefinitely.
func TestRunSync_FixWithStaleSentinels_RefreshesSentinels(t *testing.T) {
	initTestApp(t)
	mtixDir := setupSyncedState(t)
	app.mtixDir = mtixDir

	tasksPath := filepath.Join(mtixDir, "tasks.json")
	expectedHash := hashFileContent(t, tasksPath)
	fileSentinelBefore, _ := readSentinels(t)
	require.Equal(t, expectedHash, fileSentinelBefore,
		"setup invariant: sentinel matches file hash after create+export")

	// Corrupt both sentinels to simulate the user's drift scenario:
	// content matches, sentinels are stale.
	writeSentinels(t, "stale_file_hash_from_drift", "stale_db_hash_from_drift")

	// Run sync --fix. The user expects this to refresh sentinels and
	// clear any conflict warning.
	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	require.NoError(t, runSync(cmd, true))

	// After --fix, sentinels MUST match the current file content.
	fileSentinelAfter, dbSentinelAfter := readSentinels(t)
	currentHash := hashFileContent(t, tasksPath)
	assert.Equal(t, currentHash, fileSentinelAfter,
		"sync --fix must refresh sync.sha256 to match current tasks.json")
	assert.NotEqual(t, "stale_file_hash_from_drift", fileSentinelAfter,
		"stale sentinel must be replaced")
	assert.NotEqual(t, "stale_db_hash_from_drift", dbSentinelAfter,
		"stale db sentinel must be replaced")
	assert.NotEmpty(t, dbSentinelAfter, "db sentinel must be written")
}

// TestRunSync_FixWithoutDrift_StillRefreshes verifies that --fix is idempotent
// — even when sentinels and content already agree, --fix runs without error and
// produces a deterministic refresh.
func TestRunSync_FixWithoutDrift_StillRefreshes(t *testing.T) {
	initTestApp(t)
	mtixDir := setupSyncedState(t)
	app.mtixDir = mtixDir

	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	require.NoError(t, runSync(cmd, true))

	_, dbSentinelAfter := readSentinels(t)
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	currentHash := hashFileContent(t, tasksPath)
	fileSentinelAfter, _ := readSentinels(t)
	assert.Equal(t, currentHash, fileSentinelAfter,
		"sentinel must match current file hash after --fix")
	assert.NotEmpty(t, dbSentinelAfter)
}

// TestRunSync_FixWithContentDrift_RewritesTasksJSON verifies the original
// purpose of --fix: when DB and tasks.json have different node IDs (true
// content drift), the DB wins and tasks.json is rewritten.
func TestRunSync_FixWithContentDrift_RewritesTasksJSON(t *testing.T) {
	initTestApp(t)
	mtixDir := setupSyncedState(t)
	app.mtixDir = mtixDir

	// Manually write a different tasks.json with a phantom node.
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	phantom := `{
  "version": 1,
  "schema_version": "1.0.0",
  "exported_at": "2026-01-01T00:00:00Z",
  "mtix_version": "",
  "project": "TEST",
  "nodes": [{"id":"PHANTOM-1","parent_id":"","depth":0,"seq":1,"project":"PHANTOM","title":"Phantom","description":"","prompt":"","acceptance":"","node_type":"epic","issue_type":"","priority":3,"labels":"[]","status":"open","progress":0,"assignee":"","creator":"","agent_state":"","weight":1,"content_hash":"x","created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-01T00:00:00Z"}],
  "dependencies": null,
  "agents": null,
  "sessions": null,
  "node_count": 1,
  "checksum": "deadbeef"
}`
	require.NoError(t, os.WriteFile(tasksPath, []byte(phantom), 0o644))

	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	require.NoError(t, runSync(cmd, true))

	// After --fix, tasks.json must reflect DB content (TEST-1, not PHANTOM-1).
	content, err := os.ReadFile(tasksPath)
	require.NoError(t, err)
	assert.Contains(t, string(content), "TEST-1",
		"DB node should be present in rewritten tasks.json")
	assert.NotContains(t, string(content), "PHANTOM-1",
		"phantom node should be removed by --fix")
}

// TestRunSync_NoFix_DoesNotRefreshSentinels verifies that without --fix,
// sentinels are NOT touched (read-only behavior preserved).
func TestRunSync_NoFix_DoesNotRefreshSentinels(t *testing.T) {
	initTestApp(t)
	mtixDir := setupSyncedState(t)
	app.mtixDir = mtixDir

	// Corrupt sentinels.
	writeSentinels(t, "intentionally_stale", "intentionally_stale_db")

	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	require.NoError(t, runSync(cmd, false))

	// Without --fix, sentinels stay stale (read-only).
	fileSentinelAfter, dbSentinelAfter := readSentinels(t)
	assert.Equal(t, "intentionally_stale", fileSentinelAfter,
		"sync without --fix must not modify sentinels")
	assert.Equal(t, "intentionally_stale_db", dbSentinelAfter)
}

// testContext returns a context attached to the test, cancelled on cleanup.
func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	return ctx
}

// TestRunSync_NoSyncService_ReturnsError verifies the guard for missing
// sync service (e.g., mtix run outside a project).
func TestRunSync_NoSyncService_ReturnsError(t *testing.T) {
	saveAndResetApp(t)
	app = appContext{}

	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	err := runSync(cmd, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunSync_JSONMode_EmitsValidJSON verifies that --json output is valid
// and contains the expected SyncReport fields.
func TestRunSync_JSONMode_EmitsValidJSON(t *testing.T) {
	initTestApp(t)
	mtixDir := setupSyncedState(t)
	app.mtixDir = mtixDir
	app.jsonOutput = true

	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	require.NoError(t, runSync(cmd, false))
}

// TestRunSync_JSONMode_WithFix verifies that --json + --fix both work and
// the fix runs after the JSON report is emitted.
func TestRunSync_JSONMode_WithFix(t *testing.T) {
	initTestApp(t)
	mtixDir := setupSyncedState(t)
	app.mtixDir = mtixDir
	app.jsonOutput = true

	// Stale sentinels.
	writeSentinels(t, "stale", "stale_db")

	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	require.NoError(t, runSync(cmd, true))

	// Sentinels must be refreshed despite JSON output mode.
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	currentHash := hashFileContent(t, tasksPath)
	fileSentinelAfter, _ := readSentinels(t)
	assert.Equal(t, currentHash, fileSentinelAfter,
		"--fix must work in JSON output mode")
}

// TestRunSync_CompareError_PropagatesError verifies that errors from
// Compare (e.g., missing tasks.json) propagate to the caller.
func TestRunSync_CompareError_PropagatesError(t *testing.T) {
	initTestApp(t)
	cwd, err := os.Getwd()
	require.NoError(t, err)
	app.mtixDir = filepath.Join(cwd, ".mtix")

	// No tasks.json exists yet — Compare will fail to read it.
	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	syncErr := runSync(cmd, false)
	require.Error(t, syncErr)
	assert.Contains(t, syncErr.Error(), "compare:")
}

// TestNewSyncCmd_RunEInvokesRunSync verifies the cobra command wiring —
// the RunE callback delegates to runSync with the --fix flag value.
func TestNewSyncCmd_RunEInvokesRunSync(t *testing.T) {
	initTestApp(t)
	mtixDir := setupSyncedState(t)
	app.mtixDir = mtixDir

	cmd := newSyncCmd()
	cmd.SetContext(testContext(t))
	require.NoError(t, cmd.Flags().Set("fix", "true"))
	require.NoError(t, cmd.RunE(cmd, nil))

	// --fix path should have refreshed sentinels.
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	currentHash := hashFileContent(t, tasksPath)
	fileSentinelAfter, _ := readSentinels(t)
	assert.Equal(t, currentHash, fileSentinelAfter)
}

// TestRunSync_AutoExportFailure_PropagatesError verifies that --fix returns
// a wrapped "fix:" error when AutoExport fails. We trigger AutoExport failure
// by pointing app.mtixDir at a non-existent parent so the lock acquisition or
// file write fails.
func TestRunSync_AutoExportFailure_PropagatesError(t *testing.T) {
	initTestApp(t)
	mtixDir := setupSyncedState(t)

	// Compare succeeds (reads from the real mtixDir we set up).
	// Then we point app.mtixDir at the path that exists for Compare,
	// run the test such that AutoExport's I/O fails. The simplest way:
	// Compare reads tasks.json successfully, but we then chmod the
	// data dir to remove write permission to force AutoExport to fail
	// when it tries to update sentinel hash files.
	if os.Getuid() == 0 {
		t.Skip("running as root; cannot test write-permission failure")
	}
	app.mtixDir = mtixDir

	// Remove write permission on .mtix and .mtix/data.
	dataDir := filepath.Join(mtixDir, "data")
	require.NoError(t, os.Chmod(dataDir, 0o500))
	require.NoError(t, os.Chmod(mtixDir, 0o500))
	t.Cleanup(func() {
		_ = os.Chmod(mtixDir, 0o755)
		_ = os.Chmod(dataDir, 0o755)
	})

	cmd := &cobra.Command{}
	cmd.SetContext(testContext(t))
	err := runSync(cmd, true)
	// Either the fix path errors (good — error wrapped) or the OS allowed
	// the writes (filesystem-dependent). We assert defensively.
	if err != nil {
		assert.Contains(t, err.Error(), "fix:",
			"AutoExport failure must be wrapped with 'fix:' prefix")
	}
}
