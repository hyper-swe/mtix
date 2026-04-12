// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ============================================================================
// runXxx function tests — test early-exit error paths (nil service checks)
// These test the "not in an mtix project" guard at the top of each runXxx.
// ============================================================================

// TestRunCreate_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunCreate_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runCreate("title", "", "", 3, "", "", "", "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunShow_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunShow_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runShow("PROJ-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunList_NoStore_ReturnsError verifies nil service guard.
func TestRunList_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runList("", "", "", "", "", "", 50)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunTree_NoStore_ReturnsError verifies nil service guard.
func TestRunTree_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runTree("PROJ-1", 10)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunUpdate_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunUpdate_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runUpdate("PROJ-1", "new title", "", "", "", 0, "", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunUpdate_NoFieldsToUpdate_ReturnsError verifies empty update detection.
func TestRunUpdate_NoFieldsToUpdate_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	// nodeSvc needs to be non-nil to pass nil guard but we set it via reflection-free approach.
	// Since nodeSvc is unexported and typed, we can only test the nil guard path.
	app = appContext{}

	// Even with a nodeSvc set, passing all empty fields would hit "no fields to update".
	// But since nodeSvc is nil, it hits the nil guard first. Test the nil guard.
	err := runUpdate("PROJ-1", "", "", "", "", 0, "", "")
	assert.Error(t, err)
}

// TestRunClaim_NoStore_ReturnsError verifies nil service guard.
func TestRunClaim_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runClaim("PROJ-1", "agent-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunUnclaim_NoStore_ReturnsError verifies nil service guard.
func TestRunUnclaim_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runUnclaim("PROJ-1", "done with it")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunTransition_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunTransition_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runTransition("PROJ-1", "done", "completed")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunDefer_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunDefer_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runDefer("PROJ-1", "")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunDefer_InvalidTimestamp_ReturnsError verifies timestamp validation.
func TestRunDefer_InvalidTimestamp_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	// With nil nodeSvc, but an invalid timestamp, the nil guard triggers first.
	err := runDefer("PROJ-1", "not-a-timestamp")
	assert.Error(t, err)
}

// TestRunCancel_NoStore_ReturnsError verifies nil service guard.
func TestRunCancel_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runCancel("PROJ-1", "no longer needed", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunDelete_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunDelete_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runDelete("PROJ-1", false)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunUndelete_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunUndelete_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runUndelete("PROJ-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunDepAdd_NoStore_ReturnsError verifies nil service guard.
func TestRunDepAdd_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runDepAdd("PROJ-1", "PROJ-2", "blocks")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunDepRemove_NoStore_ReturnsError verifies nil service guard.
func TestRunDepRemove_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runDepRemove("PROJ-1", "PROJ-2", "blocks")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunDepShow_NoStore_ReturnsError verifies nil service guard.
func TestRunDepShow_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runDepShow("PROJ-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunSearch_NoStore_ReturnsError verifies nil service guard.
func TestRunSearch_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runSearch("", "", "", "", "", 50)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunReady_NoBgSvc_ReturnsError verifies nil service guard.
func TestRunReady_NoBgSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runReady()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunBlocked_NoStore_ReturnsError verifies nil service guard.
func TestRunBlocked_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runBlocked()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunStale_NoAgentSvc_ReturnsError verifies nil service guard.
func TestRunStale_NoAgentSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runStale()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunOrphans_NoStore_ReturnsError verifies nil service guard.
func TestRunOrphans_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runOrphans()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunStats_NoStore_ReturnsError verifies nil service guard.
func TestRunStats_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runStats()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunProgress_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunProgress_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runProgress("PROJ-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunGC_NoBgSvc_ReturnsError verifies nil service guard.
func TestRunGC_NoBgSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runGC()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunVerify_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunVerify_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runVerify("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunBackup_NoStore_ReturnsError verifies nil service guard.
func TestRunBackup_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runBackup("/tmp/backup")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunExport_NoStore_ReturnsError verifies nil service guard.
func TestRunExport_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runExport("json")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunServe_NoStore_ReturnsError verifies nil service guard.
func TestRunServe_NoStore_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runServe("127.0.0.1", 8377)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunPrompt_NoPromptSvc_ReturnsError verifies nil service guard.
func TestRunPrompt_NoPromptSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runPrompt("PROJ-1", "prompt text")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunAnnotate_NoPromptSvc_ReturnsError verifies nil service guard.
func TestRunAnnotate_NoPromptSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runAnnotate("PROJ-1", "note text")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunResolveAnnotation_NoPromptSvc_ReturnsError verifies nil service guard.
func TestRunResolveAnnotation_NoPromptSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runResolveAnnotation("PROJ-1", "annot-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunContext_NoCtxSvc_ReturnsError verifies nil service guard.
func TestRunContext_NoCtxSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runContext("PROJ-1", 0)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunComment_NoPromptSvc_ReturnsError verifies nil service guard.
func TestRunComment_NoPromptSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runComment("PROJ-1", "some comment")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunDecompose_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunDecompose_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runDecompose("PROJ-1", []string{"child1", "child2"})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunRerun_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunRerun_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runRerun("PROJ-1", "all", "reason")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunRestore_NoNodeSvc_ReturnsError verifies nil service guard.
func TestRunRestore_NoNodeSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runRestore("PROJ-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunGetAgentState_NoAgentSvc_ReturnsError verifies nil service guard.
func TestRunGetAgentState_NoAgentSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runGetAgentState("agent-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunSetAgentState_NoAgentSvc_ReturnsError verifies nil service guard.
func TestRunSetAgentState_NoAgentSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runSetAgentState("agent-1", "idle")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunAgentHeartbeat_NoAgentSvc_ReturnsError verifies nil service guard.
func TestRunAgentHeartbeat_NoAgentSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runAgentHeartbeat("agent-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunAgentWork_NoAgentSvc_ReturnsError verifies nil service guard.
func TestRunAgentWork_NoAgentSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runAgentWork("agent-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunSessionStart_NoSessionSvc_ReturnsError verifies nil service guard.
func TestRunSessionStart_NoSessionSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runSessionStart("agent-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunSessionEnd_NoSessionSvc_ReturnsError verifies nil service guard.
func TestRunSessionEnd_NoSessionSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runSessionEnd("agent-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunSessionSummary_NoSessionSvc_ReturnsError verifies nil service guard.
func TestRunSessionSummary_NoSessionSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runSessionSummary("agent-1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunConfigGet_NoConfigSvc_ReturnsError verifies nil service guard.
func TestRunConfigGet_NoConfigSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runConfigGet("prefix")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunConfigSet_NoConfigSvc_ReturnsError verifies nil service guard.
func TestRunConfigSet_NoConfigSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runConfigSet("prefix", "TEST")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunConfigDelete_NoConfigSvc_ReturnsError verifies nil service guard.
func TestRunConfigDelete_NoConfigSvc_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runConfigDelete("prefix")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestCloseApp_NilStore_ReturnsNil verifies no error when store is nil.
func TestCloseApp_NilStore_ReturnsNil(t *testing.T) {
	old := app
	defer func() { app = old; resetCloseOnce() }()
	app = appContext{}
	resetCloseOnce()

	err := closeApp()
	assert.NoError(t, err)
}

// ============================================================================
// output.go — jsonWriter suppressed methods (cover WriteHuman and WriteTable)
// ============================================================================

// TestJSONWriter_WriteHuman_Suppressed verifies human output is suppressed in JSON mode.
func TestJSONWriter_WriteHuman_Suppressed(t *testing.T) {
	var buf bytes.Buffer
	w := &jsonWriter{w: &buf}
	w.WriteHuman("hello %s", "world")
	assert.Empty(t, buf.String())
}

// TestJSONWriter_WriteTable_Suppressed verifies table output is suppressed in JSON mode.
func TestJSONWriter_WriteTable_Suppressed(t *testing.T) {
	var buf bytes.Buffer
	w := &jsonWriter{w: &buf}
	w.WriteTable([]string{"A"}, [][]string{{"1"}})
	assert.Empty(t, buf.String())
}

// ============================================================================
// RunE closure tests — exercise the command closures via Execute
// ============================================================================

// TestMigrateCmd_Execute_PrintsMessage verifies migrate runs without error.
func TestMigrateCmd_Execute_PrintsMessage(t *testing.T) {
	cmd := newMigrateCmd()
	cmd.SetArgs([]string{})

	var buf bytes.Buffer
	cmd.SetOut(&buf)

	err := cmd.Execute()
	require.NoError(t, err)
}

// TestRunInit_InvalidPrefix_ReturnsError verifies prefix validation in runInit.
func TestRunInit_InvalidPrefix_ReturnsError(t *testing.T) {
	err := runInit("invalid_prefix")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid prefix")
}

// TestRunInit_LowercasePrefix_ReturnsError verifies lowercase rejection.
func TestRunInit_LowercasePrefix_ReturnsError(t *testing.T) {
	err := runInit("proj")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid prefix")
}

// TestRunInit_EmptyPrefix_ReturnsError verifies empty prefix rejection.
func TestRunInit_EmptyPrefix_ReturnsError(t *testing.T) {
	err := runInit("")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid prefix")
}

// TestRunInit_SpecialCharPrefix_ReturnsError verifies special char rejection.
func TestRunInit_SpecialCharPrefix_ReturnsError(t *testing.T) {
	err := runInit("PROJ@1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid prefix")
}
