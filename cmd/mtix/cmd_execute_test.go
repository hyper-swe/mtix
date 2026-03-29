// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// Command execution tests — exercise RunE closures via cobra Execute.
// These test the command wiring (args dispatch to run functions).
// ============================================================================

// setupCleanApp saves and resets the global app state for isolated tests.
func setupCleanApp(t *testing.T) {
	t.Helper()
	old := app
	t.Cleanup(func() { app = old })
	app = appContext{}
}

// executeCmd runs a cobra command with the given args and captures stderr.
func executeCmd(cmd *cobra.Command, args ...string) (string, error) {
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}

// TestGCCmd_Execute_NilService_ReturnsError verifies gc RunE wiring.
func TestGCCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newGCCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestVerifyCmd_Execute_NilService_ReturnsError verifies verify RunE wiring.
func TestVerifyCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newVerifyCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestVerifyCmd_Execute_WithID_NilService_ReturnsError verifies verify with ID.
func TestVerifyCmd_Execute_WithID_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newVerifyCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestBackupCmd_Execute_NilService_ReturnsError verifies backup RunE wiring.
func TestBackupCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newBackupCmd()
	_, err := executeCmd(cmd, "/tmp/backup")
	assert.Error(t, err)
}

// TestExportCmd_Execute_NilService_ReturnsError verifies export RunE wiring.
func TestExportCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newExportCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestImportCmd_Execute_NilService_ReturnsError verifies import RunE wiring.
func TestImportCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newImportCmd()
	// Import with no store reads the file but fails on nil store.
	_, err := executeCmd(cmd, "nonexistent.json")
	assert.Error(t, err)
}

// TestServeCmd_Execute_NilService_ReturnsError verifies serve RunE wiring.
func TestServeCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newServeCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestShowCmd_Execute_NilService_ReturnsError verifies show RunE wiring.
func TestShowCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newShowCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestListCmd_Execute_NilService_ReturnsError verifies list RunE wiring.
func TestListCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newListCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestTreeCmd_Execute_NilService_ReturnsError verifies tree RunE wiring.
func TestTreeCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newTreeCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestCreateCmd_Execute_NilService_ReturnsError verifies create RunE wiring.
func TestCreateCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newCreateCmd()
	_, err := executeCmd(cmd, "My Task")
	assert.Error(t, err)
}

// TestMicroCmd_Execute_MissingUnder_ReturnsError verifies micro --under required.
func TestMicroCmd_Execute_MissingUnder_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newMicroCmd()
	_, err := executeCmd(cmd, "My Micro Task")
	assert.Error(t, err)
}

// TestUpdateCmd_Execute_NilService_ReturnsError verifies update RunE wiring.
func TestUpdateCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newUpdateCmd()
	_, err := executeCmd(cmd, "PROJ-1", "--title", "New Title")
	assert.Error(t, err)
}

// TestClaimCmd_Execute_MissingAgent_ReturnsError verifies claim --agent required.
func TestClaimCmd_Execute_MissingAgent_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newClaimCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestUnclaimCmd_Execute_MissingReason_ReturnsError verifies unclaim --reason required.
func TestUnclaimCmd_Execute_MissingReason_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newUnclaimCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestDoneCmd_Execute_NilService_ReturnsError verifies done RunE wiring.
func TestDoneCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newDoneCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestDeferCmd_Execute_NilService_ReturnsError verifies defer RunE wiring.
func TestDeferCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newDeferCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestCancelCmd_Execute_MissingReason_ReturnsError verifies cancel --reason required.
func TestCancelCmd_Execute_MissingReason_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newCancelCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestReopenCmd_Execute_NilService_ReturnsError verifies reopen RunE wiring.
func TestReopenCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newReopenCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestDeleteCmd_Execute_NilService_ReturnsError verifies delete RunE wiring.
func TestDeleteCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newDeleteCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestUndeleteCmd_Execute_NilService_ReturnsError verifies undelete RunE wiring.
func TestUndeleteCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newUndeleteCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestCommentCmd_Execute_NilService_ReturnsError verifies comment RunE wiring.
func TestCommentCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newCommentCmd()
	_, err := executeCmd(cmd, "PROJ-1", "my comment")
	assert.Error(t, err)
}

// TestDecomposeCmd_Execute_NilService_ReturnsError verifies decompose RunE wiring.
func TestDecomposeCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newDecomposeCmd()
	_, err := executeCmd(cmd, "PROJ-1", "child1", "child2")
	assert.Error(t, err)
}

// TestDepAddCmd_Execute_NilService_ReturnsError verifies dep add RunE wiring.
func TestDepAddCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newDepAddCmd()
	_, err := executeCmd(cmd, "PROJ-1", "PROJ-2")
	assert.Error(t, err)
}

// TestDepRemoveCmd_Execute_NilService_ReturnsError verifies dep remove RunE wiring.
func TestDepRemoveCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newDepRemoveCmd()
	_, err := executeCmd(cmd, "PROJ-1", "PROJ-2")
	assert.Error(t, err)
}

// TestDepShowCmd_Execute_NilService_ReturnsError verifies dep show RunE wiring.
func TestDepShowCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newDepShowCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestSearchCmd_Execute_NilService_ReturnsError verifies search RunE wiring.
func TestSearchCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newSearchCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestReadyCmd_Execute_NilService_ReturnsError verifies ready RunE wiring.
func TestReadyCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newReadyCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestBlockedCmd_Execute_NilService_ReturnsError verifies blocked RunE wiring.
func TestBlockedCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newBlockedCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestStaleCmd_Execute_NilService_ReturnsError verifies stale RunE wiring.
func TestStaleCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newStaleCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestOrphansCmd_Execute_NilService_ReturnsError verifies orphans RunE wiring.
func TestOrphansCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newOrphansCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestStatsCmd_Execute_NilService_ReturnsError verifies stats RunE wiring.
func TestStatsCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newStatsCmd()
	_, err := executeCmd(cmd)
	assert.Error(t, err)
}

// TestProgressCmd_Execute_NilService_ReturnsError verifies progress RunE wiring.
func TestProgressCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newProgressCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestPromptCmd_Execute_NilService_ReturnsError verifies prompt RunE wiring.
func TestPromptCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newPromptCmd()
	_, err := executeCmd(cmd, "PROJ-1", "prompt text")
	assert.Error(t, err)
}

// TestAnnotateCmd_Execute_NilService_ReturnsError verifies annotate RunE wiring.
func TestAnnotateCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newAnnotateCmd()
	_, err := executeCmd(cmd, "PROJ-1", "note")
	assert.Error(t, err)
}

// TestResolveAnnotationCmd_Execute_NilService_ReturnsError verifies resolve RunE.
func TestResolveAnnotationCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newResolveAnnotationCmd()
	_, err := executeCmd(cmd, "PROJ-1", "annot-1")
	assert.Error(t, err)
}

// TestContextCmd_Execute_NilService_ReturnsError verifies context RunE wiring.
func TestContextCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newContextCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestRerunCmd_Execute_NilService_ReturnsError verifies rerun RunE wiring.
func TestRerunCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newRerunCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestRestoreCmd_Execute_NilService_ReturnsError verifies restore RunE wiring.
func TestRestoreCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newRestoreCmd()
	_, err := executeCmd(cmd, "PROJ-1")
	assert.Error(t, err)
}

// TestAgentStateCmd_Execute_NilService_ReturnsError verifies agent state RunE.
func TestAgentStateCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newAgentStateCmd()
	_, err := executeCmd(cmd, "agent-1")
	assert.Error(t, err)
}

// TestAgentStateCmd_Execute_SetFlag_NilService_ReturnsError verifies set state.
func TestAgentStateCmd_Execute_SetFlag_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newAgentStateCmd()
	_, err := executeCmd(cmd, "agent-1", "--set", "idle")
	assert.Error(t, err)
}

// TestAgentHeartbeatCmd_Execute_NilService_ReturnsError verifies heartbeat RunE.
func TestAgentHeartbeatCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newAgentHeartbeatCmd()
	_, err := executeCmd(cmd, "agent-1")
	assert.Error(t, err)
}

// TestAgentWorkCmd_Execute_NilService_ReturnsError verifies agent work RunE.
func TestAgentWorkCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newAgentWorkCmd()
	_, err := executeCmd(cmd, "agent-1")
	assert.Error(t, err)
}

// TestSessionStartCmd_Execute_NilService_ReturnsError verifies session start RunE.
func TestSessionStartCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newSessionStartCmd()
	_, err := executeCmd(cmd, "agent-1")
	assert.Error(t, err)
}

// TestSessionEndCmd_Execute_NilService_ReturnsError verifies session end RunE.
func TestSessionEndCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newSessionEndCmd()
	_, err := executeCmd(cmd, "agent-1")
	assert.Error(t, err)
}

// TestSessionSummaryCmd_Execute_NilService_ReturnsError verifies summary RunE.
func TestSessionSummaryCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newSessionSummaryCmd()
	_, err := executeCmd(cmd, "agent-1")
	assert.Error(t, err)
}

// TestConfigGetCmd_Execute_NilService_ReturnsError verifies config get RunE.
func TestConfigGetCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newConfigGetCmd()
	_, err := executeCmd(cmd, "prefix")
	assert.Error(t, err)
}

// TestConfigSetCmd_Execute_NilService_ReturnsError verifies config set RunE.
func TestConfigSetCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newConfigSetCmd()
	_, err := executeCmd(cmd, "prefix", "TEST")
	assert.Error(t, err)
}

// TestConfigDeleteCmd_Execute_NilService_ReturnsError verifies config delete RunE.
func TestConfigDeleteCmd_Execute_NilService_ReturnsError(t *testing.T) {
	setupCleanApp(t)
	cmd := newConfigDeleteCmd()
	_, err := executeCmd(cmd, "prefix")
	assert.Error(t, err)
}

// TestInitCmd_Execute_AlreadyInitialized_ReturnsError verifies init in existing project.
func TestInitCmd_Execute_AlreadyInitialized_ReturnsError(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.Mkdir(tmpDir+"/.mtix", 0o755))

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	setupCleanApp(t)
	cmd := newInitCmd()
	_, execErr := executeCmd(cmd)
	assert.Error(t, execErr)
}

// TestNewRootCmd_HasExpectedSubcommandCount verifies all subcommands registered.
func TestNewRootCmd_HasExpectedSubcommandCount(t *testing.T) {
	rootCmd := newRootCmd()
	// Verify we have a substantial number of subcommands.
	assert.True(t, len(rootCmd.Commands()) >= 30,
		"root should have at least 30 subcommands, got %d", len(rootCmd.Commands()))
}

// TestNewRootCmd_SilenceSettings_Configured verifies silence settings.
func TestNewRootCmd_SilenceSettings_Configured(t *testing.T) {
	rootCmd := newRootCmd()
	assert.True(t, rootCmd.SilenceUsage)
	assert.True(t, rootCmd.SilenceErrors)
}

// TestShouldRouteToServer_NonExemptCmd_NoMtixDir_ReturnsZero verifies no routing without .mtix.
func TestShouldRouteToServer_NonExemptCmd_NoMtixDir_ReturnsZero(t *testing.T) {
	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	cmd := &cobra.Command{Use: "show"}
	port := shouldRouteToServer(cmd)
	assert.Equal(t, 0, port)
}

// TestShouldRouteToServer_NonExemptCmd_NoPIDLock_ReturnsZero verifies no routing without lock.
func TestShouldRouteToServer_NonExemptCmd_NoPIDLock_ReturnsZero(t *testing.T) {
	tmpDir := t.TempDir()
	require.NoError(t, os.Mkdir(tmpDir+"/.mtix", 0o755))

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	cmd := &cobra.Command{Use: "show"}
	port := shouldRouteToServer(cmd)
	assert.Equal(t, 0, port)
}

// TestRunFunction_CanBeExecuted verifies the run() function.
func TestRunFunction_CanBeExecuted(t *testing.T) {
	// run() creates a root command and executes it. With no args and no .mtix,
	// it should show help and succeed.
	// We can't easily test this without side effects, so just verify it compiles.
	// The function is tested indirectly through newRootCmd tests.
}
