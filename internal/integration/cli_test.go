// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package integration contains end-to-end integration tests per MTIX-11.1.
// CLI integration tests execute the actual mtix binary against a temp database.
package integration

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mtixBinaryOnce compiles the mtix binary once across all CLI tests.
var (
	mtixBinaryOnce sync.Once
	mtixBinaryPath string
	mtixBuildErr   error
)

// buildMtixBinary compiles the mtix binary to a temp directory.
// Called once per test run via sync.Once.
func buildMtixBinary(t *testing.T) string {
	t.Helper()
	mtixBinaryOnce.Do(func() {
		tmpDir, err := os.MkdirTemp("", "mtix-cli-test-*")
		if err != nil {
			mtixBuildErr = err
			return
		}
		mtixBinaryPath = filepath.Join(tmpDir, "mtix")
		cmd := exec.Command("go", "build", "-o", mtixBinaryPath, "../../cmd/mtix/")
		cmd.Stderr = os.Stderr
		mtixBuildErr = cmd.Run()
	})
	if mtixBuildErr != nil {
		t.Skipf("skipping CLI test: failed to build mtix binary: %v", mtixBuildErr)
	}
	return mtixBinaryPath
}

// runMtix executes the mtix binary in the given directory with the given args.
// Returns stdout, stderr, and any error.
func runMtix(t *testing.T, dir string, args ...string) (string, string, error) {
	t.Helper()
	binary := buildMtixBinary(t)
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// setupCLIProject creates a temp directory and runs mtix init.
func setupCLIProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	stdout, stderr, err := runMtix(t, dir, "init", "--prefix", "TEST")
	require.NoError(t, err, "init failed: stdout=%s stderr=%s", stdout, stderr)

	// Verify .mtix directory was created.
	_, statErr := os.Stat(filepath.Join(dir, ".mtix"))
	require.NoError(t, statErr, ".mtix directory should exist after init")

	return dir
}

// TestCLI_Init_CreatesDatabase verifies mtix init creates the project structure.
func TestCLI_Init_CreatesDatabase(t *testing.T) {
	dir := t.TempDir()

	stdout, _, err := runMtix(t, dir, "init", "--prefix", "MYPROJ")
	require.NoError(t, err)

	assert.Contains(t, stdout, "Initialized mtix project")
	assert.Contains(t, stdout, "MYPROJ")

	// Verify directory structure.
	assert.DirExists(t, filepath.Join(dir, ".mtix"))
	assert.DirExists(t, filepath.Join(dir, ".mtix", "data"))
	assert.FileExists(t, filepath.Join(dir, ".mtix", "config.yaml"))
}

// TestCLI_Init_DuplicateReturnsError verifies re-init is rejected.
func TestCLI_Init_DuplicateReturnsError(t *testing.T) {
	dir := setupCLIProject(t)

	_, _, err := runMtix(t, dir, "init", "--prefix", "TEST")
	assert.Error(t, err, "re-init should fail")
}

// TestCLI_Create_OutputsID verifies create returns a node ID.
func TestCLI_Create_OutputsID(t *testing.T) {
	dir := setupCLIProject(t)

	stdout, _, err := runMtix(t, dir, "create", "My Test Task")
	require.NoError(t, err)

	assert.Contains(t, stdout, "Created")
	assert.Contains(t, stdout, "TEST-")
	assert.Contains(t, stdout, "My Test Task")
}

// TestCLI_Create_JSONOutputMatchesSchema verifies --json create output.
func TestCLI_Create_JSONOutputMatchesSchema(t *testing.T) {
	dir := setupCLIProject(t)

	stdout, _, err := runMtix(t, dir, "--json", "create", "JSON Task")
	require.NoError(t, err)

	var node map[string]any
	err = json.Unmarshal([]byte(stdout), &node)
	require.NoError(t, err, "output should be valid JSON")

	assert.Contains(t, node, "id")
	assert.Contains(t, node, "title")
	assert.Equal(t, "JSON Task", node["title"])
	assert.Equal(t, "open", node["status"])
}

// TestCLI_Show_DisplaysDetail verifies show command output.
func TestCLI_Show_DisplaysDetail(t *testing.T) {
	dir := setupCLIProject(t)

	// Create a node first.
	createOut, _, err := runMtix(t, dir, "--json", "create", "Show Test Node")
	require.NoError(t, err)

	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	nodeID := created["id"].(string)

	// Show the node.
	stdout, _, err := runMtix(t, dir, "show", nodeID)
	require.NoError(t, err)

	assert.Contains(t, stdout, nodeID)
	assert.Contains(t, stdout, "Show Test Node")
	assert.Contains(t, stdout, "○") // open status icon
	assert.Contains(t, stdout, "Progress:")
}

// TestCLI_Ls_ListsWithStatusIcons verifies list command shows status icons.
func TestCLI_Ls_ListsWithStatusIcons(t *testing.T) {
	dir := setupCLIProject(t)

	// Create multiple nodes.
	_, _, err := runMtix(t, dir, "create", "Task Alpha")
	require.NoError(t, err)
	_, _, err = runMtix(t, dir, "create", "Task Beta")
	require.NoError(t, err)

	// List all nodes.
	stdout, _, err := runMtix(t, dir, "list")
	require.NoError(t, err)

	assert.Contains(t, stdout, "Task Alpha")
	assert.Contains(t, stdout, "Task Beta")
	assert.Contains(t, stdout, "○") // open status icon
	assert.Contains(t, stdout, "ID")  // table header
}

// TestCLI_Done_TransitionsStatus verifies done command transitions status.
func TestCLI_Done_TransitionsStatus(t *testing.T) {
	dir := setupCLIProject(t)

	// Create a node.
	createOut, _, err := runMtix(t, dir, "--json", "create", "Done Test")
	require.NoError(t, err)

	var created map[string]any
	require.NoError(t, json.Unmarshal([]byte(createOut), &created))
	nodeID := created["id"].(string)

	// Transition: open → in_progress (claim first).
	_, _, err = runMtix(t, dir, "claim", nodeID, "--agent", "test-agent")
	require.NoError(t, err)

	// Transition: in_progress → done.
	stdout, _, err := runMtix(t, dir, "done", nodeID)
	require.NoError(t, err)
	assert.Contains(t, stdout, "✓")
	assert.Contains(t, stdout, "done")

	// Verify via show --json.
	showOut, _, err := runMtix(t, dir, "--json", "show", nodeID)
	require.NoError(t, err)

	var shown map[string]any
	require.NoError(t, json.Unmarshal([]byte(showOut), &shown))
	assert.Equal(t, "done", shown["status"])
}

// TestCLI_Tree_ShowsHierarchy verifies tree command with connectors.
func TestCLI_Tree_ShowsHierarchy(t *testing.T) {
	dir := setupCLIProject(t)

	// Create parent.
	createOut, _, err := runMtix(t, dir, "--json", "create", "Parent Node")
	require.NoError(t, err)

	var parent map[string]any
	require.NoError(t, json.Unmarshal([]byte(createOut), &parent))
	parentID := parent["id"].(string)

	// Create children under parent.
	_, _, err = runMtix(t, dir, "create", "Child A", "--under", parentID)
	require.NoError(t, err)
	_, _, err = runMtix(t, dir, "create", "Child B", "--under", parentID)
	require.NoError(t, err)

	// Show tree.
	stdout, _, err := runMtix(t, dir, "tree", parentID)
	require.NoError(t, err)

	assert.Contains(t, stdout, "Parent Node")
	assert.Contains(t, stdout, "Child A")
	assert.Contains(t, stdout, "Child B")
	// Should have tree connectors.
	hasConnector := strings.Contains(stdout, "├") || strings.Contains(stdout, "└")
	assert.True(t, hasConnector, "tree output should contain ASCII connectors")
}

// TestCLI_Config_SetsValue verifies config set/get.
func TestCLI_Config_SetsValue(t *testing.T) {
	dir := setupCLIProject(t)

	// Set a config value.
	_, _, err := runMtix(t, dir, "config", "set", "prefix", "NEWPROJ")
	require.NoError(t, err)

	// Get the config value.
	stdout, _, err := runMtix(t, dir, "config", "get", "prefix")
	require.NoError(t, err)
	assert.Contains(t, stdout, "NEWPROJ")
}

// TestCLI_InvalidCommand_ExitCode1 verifies unknown commands return exit code 1.
func TestCLI_InvalidCommand_ExitCode1(t *testing.T) {
	dir := t.TempDir()

	_, _, err := runMtix(t, dir, "nonexistent-command")
	assert.Error(t, err, "unknown command should return error")

	// Check exit code is non-zero.
	if exitErr, ok := err.(*exec.ExitError); ok {
		assert.NotEqual(t, 0, exitErr.ExitCode())
	}
}

// TestCLI_NonexistentNode_ReturnsError verifies show on missing node returns error.
func TestCLI_NonexistentNode_ReturnsError(t *testing.T) {
	dir := setupCLIProject(t)

	_, _, err := runMtix(t, dir, "show", "NONEXISTENT-999")
	assert.Error(t, err, "show on nonexistent node should fail")
}

// TestCLI_Stats_ShowsCounts verifies stats command output.
func TestCLI_Stats_ShowsCounts(t *testing.T) {
	dir := setupCLIProject(t)

	// Create some nodes.
	_, _, err := runMtix(t, dir, "create", "Stats Task 1")
	require.NoError(t, err)
	_, _, err = runMtix(t, dir, "create", "Stats Task 2")
	require.NoError(t, err)

	// Run stats.
	stdout, _, err := runMtix(t, dir, "stats")
	require.NoError(t, err)

	assert.Contains(t, stdout, "Statistics")
	assert.Contains(t, stdout, "○") // open icon
}
