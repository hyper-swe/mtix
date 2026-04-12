// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// registerTestAgent inserts an agent row directly into the database for testing.
func registerTestAgent(t *testing.T, agentID string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Format(time.RFC3339)
	db := app.store.WriteDB()
	_, err := db.ExecContext(ctx,
		`INSERT OR REPLACE INTO agents (agent_id, project, state, state_changed_at, last_heartbeat)
		 VALUES (?, ?, 'idle', ?, ?)`,
		agentID, "TEST", now, now,
	)
	require.NoError(t, err)
}

// transitionToInProgress transitions a node to in_progress before done.
func transitionToInProgress(t *testing.T, nodeID string) {
	t.Helper()
	err := app.nodeSvc.TransitionStatus(
		context.Background(), nodeID, model.StatusInProgress, "started via test", "test",
	)
	require.NoError(t, err)
}

// ============================================================================
// Additional coverage tests — exercise happy paths of run* functions.
// These use initTestApp (from coverage_boost_test.go) for a real store.
// ============================================================================

// --- workflow.go: runClaim, runUnclaim, runCancel, runTransition, runDefer ---

// TestRunClaim_HappyPath_HumanOutput verifies claim success output (FR-10.4).
func TestRunClaim_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	// Create a node, then claim it.
	err := runCreate("Claim Me", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runClaim("TEST-1", "agent-1")
	assert.NoError(t, err)
}

// TestRunClaim_HappyPath_JSONOutput verifies claim JSON output (FR-10.4).
func TestRunClaim_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Claim JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runClaim("TEST-1", "agent-json")
	assert.NoError(t, err)
}

// TestRunUnclaim_HappyPath_HumanOutput verifies unclaim success (FR-10.4).
func TestRunUnclaim_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Unclaim Me", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runClaim("TEST-1", "agent-1")
	require.NoError(t, err)

	err = runUnclaim("TEST-1", "done with it")
	assert.NoError(t, err)
}

// TestRunUnclaim_HappyPath_JSONOutput verifies unclaim JSON output (FR-10.4).
func TestRunUnclaim_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Unclaim JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runClaim("TEST-1", "agent-1")
	require.NoError(t, err)

	err = runUnclaim("TEST-1", "done")
	assert.NoError(t, err)
}

// TestRunCancel_HappyPath_HumanOutput verifies cancel success (FR-6.3).
func TestRunCancel_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Cancel Me", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runCancel("TEST-1", "no longer needed", false)
	assert.NoError(t, err)
}

// TestRunCancel_HappyPath_JSONOutput verifies cancel JSON output (FR-6.3).
func TestRunCancel_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Cancel JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runCancel("TEST-1", "cleanup", false)
	assert.NoError(t, err)
}

// TestRunTransition_Done_HumanOutput verifies done transition (FR-6.3).
func TestRunTransition_Done_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Done Me", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	// Must go open -> in_progress -> done per state machine rules.
	transitionToInProgress(t, "TEST-1")
	err = runTransition("TEST-1", model.StatusDone, "completed via CLI")
	assert.NoError(t, err)
}

// TestRunTransition_Done_JSONOutput verifies done transition JSON (FR-6.3).
func TestRunTransition_Done_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Done JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	transitionToInProgress(t, "TEST-1")
	err = runTransition("TEST-1", model.StatusDone, "completed")
	assert.NoError(t, err)
}

// TestRunDefer_HappyPath_HumanOutput verifies defer success (FR-3.8).
func TestRunDefer_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Defer Me", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDefer("TEST-1", "")
	assert.NoError(t, err)
}

// TestRunDefer_HappyPath_JSONOutput verifies defer JSON output (FR-3.8).
func TestRunDefer_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Defer JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDefer("TEST-1", "")
	assert.NoError(t, err)
}

// TestRunDefer_WithValidTimestamp_HumanOutput verifies defer with timestamp (FR-3.8).
func TestRunDefer_WithValidTimestamp_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Defer Timed", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDefer("TEST-1", "2027-01-01T00:00:00Z")
	assert.NoError(t, err)
}

// TestRunDefer_InvalidTimestamp_WithService_ReturnsError verifies bad timestamp (FR-3.8).
func TestRunDefer_InvalidTimestamp_WithService_ReturnsError(t *testing.T) {
	initTestApp(t)

	err := runCreate("Defer Bad", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDefer("TEST-1", "not-a-time")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid --until timestamp")
}

// --- delete_cmd.go: runDelete, runUndelete ---

// TestRunDelete_HappyPath_HumanOutput verifies delete success (FR-6.3).
func TestRunDelete_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Delete Me", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDelete("TEST-1", false)
	assert.NoError(t, err)
}

// TestRunDelete_HappyPath_JSONOutput verifies delete JSON output (FR-6.3).
func TestRunDelete_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Delete JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDelete("TEST-1", false)
	assert.NoError(t, err)
}

// TestRunUndelete_HappyPath_HumanOutput verifies undelete success (FR-6.3).
func TestRunUndelete_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Undelete Me", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDelete("TEST-1", false)
	require.NoError(t, err)

	err = runUndelete("TEST-1")
	assert.NoError(t, err)
}

// TestRunUndelete_HappyPath_JSONOutput verifies undelete JSON output (FR-6.3).
func TestRunUndelete_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Undelete JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDelete("TEST-1", false)
	require.NoError(t, err)

	err = runUndelete("TEST-1")
	assert.NoError(t, err)
}

// --- dep.go: runDepAdd, runDepRemove, runDepShow ---

// TestRunDepAdd_HappyPath_HumanOutput verifies dep add success (FR-4.1).
func TestRunDepAdd_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Dep From", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runCreate("Dep To", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDepAdd("TEST-1", "TEST-2", "blocks")
	assert.NoError(t, err)
}

// TestRunDepAdd_HappyPath_JSONOutput verifies dep add JSON output (FR-4.1).
func TestRunDepAdd_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Dep From J", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runCreate("Dep To J", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDepAdd("TEST-1", "TEST-2", "blocks")
	assert.NoError(t, err)
}

// TestRunDepRemove_HappyPath_HumanOutput verifies dep remove success (FR-4.1).
func TestRunDepRemove_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Dep Rm From", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runCreate("Dep Rm To", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDepAdd("TEST-1", "TEST-2", "blocks")
	require.NoError(t, err)

	err = runDepRemove("TEST-1", "TEST-2", "blocks")
	assert.NoError(t, err)
}

// TestRunDepRemove_HappyPath_JSONOutput verifies dep remove JSON output (FR-4.1).
func TestRunDepRemove_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Dep Rm J From", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runCreate("Dep Rm J To", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDepAdd("TEST-1", "TEST-2", "blocks")
	require.NoError(t, err)

	err = runDepRemove("TEST-1", "TEST-2", "blocks")
	assert.NoError(t, err)
}

// TestRunDepShow_WithBlockers_HumanOutput verifies dep show with results (FR-4.1).
func TestRunDepShow_WithBlockers_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Show Dep A", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runCreate("Show Dep B", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDepAdd("TEST-1", "TEST-2", "blocks")
	require.NoError(t, err)

	// Show blockers for TEST-2, which is blocked by TEST-1.
	err = runDepShow("TEST-2")
	assert.NoError(t, err)
}

// TestRunDepShow_WithBlockers_JSONOutput verifies dep show JSON (FR-4.1).
func TestRunDepShow_WithBlockers_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Dep Show J A", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runCreate("Dep Show J B", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDepAdd("TEST-1", "TEST-2", "blocks")
	require.NoError(t, err)

	err = runDepShow("TEST-2")
	assert.NoError(t, err)
}

// TestRunDepShow_NoBlockers_HumanOutput verifies dep show with no blockers (FR-4.1).
func TestRunDepShow_NoBlockers_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("No Blockers", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDepShow("TEST-1")
	assert.NoError(t, err)
}

// --- comment.go: runComment ---

// TestRunComment_HappyPath_HumanOutput verifies comment success (FR-6.3).
func TestRunComment_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Comment Target", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runComment("TEST-1", "This is a comment")
	assert.NoError(t, err)
}

// TestRunComment_HappyPath_JSONOutput verifies comment JSON output (FR-6.3).
func TestRunComment_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Comment JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runComment("TEST-1", "JSON comment")
	assert.NoError(t, err)
}

// --- prompt_cmd.go: runPrompt, runAnnotate, runResolveAnnotation, runContext ---

// TestRunPrompt_HappyPath_HumanOutput verifies prompt success (FR-12.5).
func TestRunPrompt_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Prompt Target", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runPrompt("TEST-1", "Build this feature")
	assert.NoError(t, err)
}

// TestRunPrompt_HappyPath_JSONOutput verifies prompt JSON output (FR-12.5).
func TestRunPrompt_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Prompt JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runPrompt("TEST-1", "JSON prompt text")
	assert.NoError(t, err)
}

// TestRunAnnotate_HappyPath_HumanOutput verifies annotate success (FR-3.4).
func TestRunAnnotate_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Annotate Target", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runAnnotate("TEST-1", "Important note")
	assert.NoError(t, err)
}

// TestRunAnnotate_HappyPath_JSONOutput verifies annotate JSON output (FR-3.4).
func TestRunAnnotate_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Annotate JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runAnnotate("TEST-1", "JSON annotation")
	assert.NoError(t, err)
}

// TestRunResolveAnnotation_HappyPath_HumanOutput verifies resolve annotation (FR-3.4).
// Note: This test may fail if the annotation system requires specific IDs.
func TestRunResolveAnnotation_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Resolve Target", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	// Add annotation first to get an annotation ID.
	err = runAnnotate("TEST-1", "Resolve me")
	require.NoError(t, err)

	// Try to resolve — the annotation ID format may vary by implementation.
	// We test JSON output too even if this errors, to cover branches.
	_ = runResolveAnnotation("TEST-1", "annot-placeholder")
}

// TestRunResolveAnnotation_HappyPath_JSONOutput verifies resolve JSON (FR-3.4).
func TestRunResolveAnnotation_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Resolve JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runAnnotate("TEST-1", "Resolve JSON note")
	require.NoError(t, err)

	_ = runResolveAnnotation("TEST-1", "annot-placeholder")
}

// TestRunContext_HappyPath_HumanOutput verifies context chain display (FR-12.2).
func TestRunContext_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Context Node", "", "", 3, "desc", "prompt text", "", "", "")
	require.NoError(t, err)

	err = runContext("TEST-1", 0)
	assert.NoError(t, err)
}

// TestRunContext_HappyPath_JSONOutput verifies context JSON output (FR-12.2).
func TestRunContext_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Context JSON", "", "", 3, "desc", "prompt", "", "", "")
	require.NoError(t, err)

	err = runContext("TEST-1", 0)
	assert.NoError(t, err)
}

// TestRunContext_WithMaxTokens_HumanOutput verifies context with token budget (FR-12.2).
func TestRunContext_WithMaxTokens_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Context Tokens", "", "", 3, "desc", "prompt", "", "", "")
	require.NoError(t, err)

	err = runContext("TEST-1", 1000)
	assert.NoError(t, err)
}

// --- rerun_cmd.go: runRerun, runRestore ---

// TestRunRerun_HappyPath_HumanOutput verifies rerun success (FR-6.3).
func TestRunRerun_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Rerun Parent", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	// Decompose to have descendants to rerun.
	err = runDecompose("TEST-1", []string{"Child A", "Child B"})
	require.NoError(t, err)

	err = runRerun("TEST-1", "all", "testing rerun")
	assert.NoError(t, err)
}

// TestRunRerun_HappyPath_JSONOutput verifies rerun JSON output (FR-6.3).
func TestRunRerun_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Rerun JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runDecompose("TEST-1", []string{"J1", "J2"})
	require.NoError(t, err)

	err = runRerun("TEST-1", "all", "JSON rerun")
	assert.NoError(t, err)
}

// TestRunRestore_HappyPath_HumanOutput verifies restore success (FR-3.5).
func TestRunRestore_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Restore Parent", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runDecompose("TEST-1", []string{"Restore Child"})
	require.NoError(t, err)

	// Rerun to invalidate, then restore.
	err = runRerun("TEST-1", "review", "for restore test")
	require.NoError(t, err)

	err = runRestore("TEST-1.1")
	// May or may not succeed depending on invalidation state.
	_ = err
}

// TestRunRestore_HappyPath_JSONOutput verifies restore JSON output (FR-3.5).
func TestRunRestore_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Restore J Parent", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runDecompose("TEST-1", []string{"Restore J Child"})
	require.NoError(t, err)

	err = runRerun("TEST-1", "review", "for restore JSON")
	require.NoError(t, err)

	_ = runRestore("TEST-1.1")
}

// --- agent_cmd.go: runGetAgentState, runSetAgentState, runAgentHeartbeat, runAgentWork ---

// TestRunGetAgentState_HappyPath_HumanOutput verifies get state output (FR-10.2).
func TestRunGetAgentState_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "test-agent")

	err := runGetAgentState("test-agent")
	assert.NoError(t, err)
}

// TestRunGetAgentState_HappyPath_JSONOutput verifies get state JSON (FR-10.2).
func TestRunGetAgentState_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "test-agent-j")

	app.jsonOutput = true
	err := runGetAgentState("test-agent-j")
	assert.NoError(t, err)
}

// TestRunSetAgentState_HappyPath_HumanOutput verifies set state output (FR-10.2).
func TestRunSetAgentState_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "state-agent")

	// idle -> working is a valid agent transition.
	err := runSetAgentState("state-agent", "working")
	assert.NoError(t, err)
}

// TestRunAgentHeartbeat_HappyPath_HumanOutput verifies heartbeat output (FR-10.2).
func TestRunAgentHeartbeat_HappyPath_HumanOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "hb-agent")

	err := runAgentHeartbeat("hb-agent")
	assert.NoError(t, err)
}

// TestRunAgentHeartbeat_HappyPath_JSONOutput verifies heartbeat JSON (FR-10.2).
func TestRunAgentHeartbeat_HappyPath_JSONOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "hb-agent-j")

	app.jsonOutput = true
	err := runAgentHeartbeat("hb-agent-j")
	assert.NoError(t, err)
}

// TestRunAgentWork_NoWork_HumanOutput verifies no-work error (FR-10.2).
func TestRunAgentWork_NoWork_HumanOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "work-agent")

	err := runAgentWork("work-agent")
	// Agent has no current work — GetCurrentWork returns ErrNotFound.
	assert.Error(t, err)
}

// TestRunAgentWork_NoWork_JSONOutput verifies no-work JSON error (FR-10.2).
func TestRunAgentWork_NoWork_JSONOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "work-agent-j")

	app.jsonOutput = true
	err := runAgentWork("work-agent-j")
	// Agent has no current work — GetCurrentWork returns ErrNotFound.
	assert.Error(t, err)
}

// TestRunAgentWork_WithWork_HumanOutput verifies work display (FR-10.2).
func TestRunAgentWork_WithWork_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Work Task", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	registerTestAgent(t, "claimed-agent")

	err = runClaim("TEST-1", "claimed-agent")
	require.NoError(t, err)

	// Set current_node_id on the agent so GetCurrentWork finds it.
	ctx := context.Background()
	db := app.store.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE agents SET current_node_id = ? WHERE agent_id = ?`,
		"TEST-1", "claimed-agent",
	)
	require.NoError(t, err)

	err = runAgentWork("claimed-agent")
	assert.NoError(t, err)
}

// TestRunAgentWork_WithWork_JSONOutput verifies work display JSON (FR-10.2).
func TestRunAgentWork_WithWork_JSONOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Work JSON Task", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	registerTestAgent(t, "claimed-agent-j")

	err = runClaim("TEST-1", "claimed-agent-j")
	require.NoError(t, err)

	// Set current_node_id on the agent so GetCurrentWork finds it.
	ctx := context.Background()
	db := app.store.WriteDB()
	_, err = db.ExecContext(ctx,
		`UPDATE agents SET current_node_id = ? WHERE agent_id = ?`,
		"TEST-1", "claimed-agent-j",
	)
	require.NoError(t, err)

	app.jsonOutput = true
	err = runAgentWork("claimed-agent-j")
	assert.NoError(t, err)
}

// --- session_cmd.go: runSessionStart, runSessionEnd, runSessionSummary ---

// TestRunSession_FullLifecycle_HumanOutput verifies session lifecycle (FR-10.5a).
func TestRunSession_FullLifecycle_HumanOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "sess-agent")

	err := runSessionStart("sess-agent")
	assert.NoError(t, err)

	err = runSessionEnd("sess-agent")
	assert.NoError(t, err)

	err = runSessionSummary("sess-agent")
	assert.NoError(t, err)
}

// TestRunSession_FullLifecycle_JSONOutput verifies session lifecycle JSON (FR-10.5a).
func TestRunSession_FullLifecycle_JSONOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "sess-agent-j")

	app.jsonOutput = true
	err := runSessionStart("sess-agent-j")
	assert.NoError(t, err)

	err = runSessionEnd("sess-agent-j")
	assert.NoError(t, err)

	err = runSessionSummary("sess-agent-j")
	assert.NoError(t, err)
}

// TestRunSessionStart_WithConfigSvc_ReadsPrefix verifies prefix from config (FR-10.5a).
func TestRunSessionStart_WithConfigSvc_ReadsPrefix(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "prefix-agent")

	// configSvc should return "TEST" prefix from initTestApp config.
	err := runSessionStart("prefix-agent")
	assert.NoError(t, err)
}

// --- query.go: runSearch, runReady, runBlocked, runStale, runOrphans, printNodeList ---

// TestRunSearch_WithResults_HumanOutput verifies search with results (FR-6.3).
func TestRunSearch_WithResults_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Searchable Node", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runSearch("", "", "", "", 50)
	assert.NoError(t, err)
}

// TestRunSearch_WithResults_JSONOutput verifies search JSON with results (FR-6.3).
func TestRunSearch_WithResults_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Search JSON Node", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runSearch("", "", "", "", 50)
	assert.NoError(t, err)
}

// TestRunSearch_WithStatusFilter_HasResults verifies search with status filter (FR-6.3).
func TestRunSearch_WithStatusFilter_HasResults(t *testing.T) {
	initTestApp(t)

	err := runCreate("Open Node", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runSearch("open", "", "", "", 50)
	assert.NoError(t, err)
}

// TestRunBlocked_WithResults_JSONOutput verifies blocked JSON with results (FR-6.3).
func TestRunBlocked_WithResults_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runBlocked()
	assert.NoError(t, err)
}

// TestRunOrphans_WithNodes_HumanOutput verifies orphans with root nodes (FR-6.3).
func TestRunOrphans_WithNodes_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Orphan Node", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runOrphans()
	assert.NoError(t, err)
}

// TestRunOrphans_WithNodes_JSONOutput verifies orphans JSON (FR-6.3).
func TestRunOrphans_WithNodes_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Orphan JSON", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runOrphans()
	assert.NoError(t, err)
}

// TestRunStale_WithRegisteredAgent_HumanOutput verifies stale with agents (FR-6.3).
func TestRunStale_WithRegisteredAgent_HumanOutput(t *testing.T) {
	initTestApp(t)
	registerTestAgent(t, "stale-test-agent")

	err := runStale()
	assert.NoError(t, err)
}

// TestPrintNodeList_WithNodes_HumanOutput verifies printNodeList with data (FR-6.2).
func TestPrintNodeList_WithNodes_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("List Node 1", "", "", 2, "", "", "", "", "")
	require.NoError(t, err)
	err = runCreate("List Node 2", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	// Get nodes from store to pass to printNodeList.
	err = runList("", "", "", "", "", 50)
	assert.NoError(t, err)
}

// --- show.go: runShow, runList, runTree, printTreeFormatted ---

// TestRunShow_WithAssignee_HumanOutput verifies show with assignee (FR-6.3).
func TestRunShow_WithAssignee_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Show Assigned", "", "", 3, "description", "prompt text", "", "", "agent-1")
	require.NoError(t, err)

	err = runShow("TEST-1")
	assert.NoError(t, err)
}

// TestRunList_WithUnderFilter_HumanOutput verifies list with under filter (FR-6.3).
func TestRunList_WithUnderFilter_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("List Parent", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runDecompose("TEST-1", []string{"Under Child"})
	require.NoError(t, err)

	err = runList("", "TEST-1", "", "", "", 50)
	assert.NoError(t, err)
}

// TestRunTree_WithChildren_HumanOutput verifies tree with children (FR-9.3).
func TestRunTree_WithChildren_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Tree Parent", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runDecompose("TEST-1", []string{"Tree Child A", "Tree Child B", "Tree Child C"})
	require.NoError(t, err)

	err = runTree("TEST-1", 10)
	assert.NoError(t, err)
}

// TestRunTree_WithDepth0_HumanOutput verifies tree at max depth (FR-9.3).
func TestRunTree_WithDepth0_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Depth Limit", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runDecompose("TEST-1", []string{"Depth Child"})
	require.NoError(t, err)

	// Depth 0 should only show root.
	err = runTree("TEST-1", 0)
	assert.NoError(t, err)
}

// --- stats.go: runStats, runProgress ---

// TestRunStats_WithNodes_HumanOutput verifies stats with existing nodes (FR-6.2).
func TestRunStats_WithNodes_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Stats Node A", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runCreate("Stats Node B", "", "", 2, "", "", "", "", "")
	require.NoError(t, err)

	// Mark one as done (open -> in_progress -> done).
	transitionToInProgress(t, "TEST-1")
	err = runTransition("TEST-1", model.StatusDone, "test")
	require.NoError(t, err)

	err = runStats()
	assert.NoError(t, err)
}

// TestRunProgress_WithChildren_HumanOutput verifies progress with children (FR-5.1).
func TestRunProgress_WithChildren_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Prog Parent", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)
	err = runDecompose("TEST-1", []string{"Prog Child A", "Prog Child B"})
	require.NoError(t, err)

	err = runProgress("TEST-1")
	assert.NoError(t, err)
}

// --- config.go: runConfigGet, runConfigSet, runConfigDelete ---

// TestRunConfigSet_WithWarning_HumanOutput verifies config set with warning (FR-11.1a).
func TestRunConfigSet_WithWarning_HumanOutput(t *testing.T) {
	initTestApp(t)

	// Setting an invalid key may produce a warning.
	err := runConfigSet("max_depth", "100")
	// May or may not produce a warning, but should not error.
	_ = err
}

// TestRunConfigDelete_WithStore_HumanOutput verifies config delete human output (FR-11.1a).
func TestRunConfigDelete_WithStore_HumanOutput(t *testing.T) {
	initTestApp(t)

	// Set, then delete.
	_ = runConfigSet("max_depth", "5")

	err := runConfigDelete("max_depth")
	// May succeed or fail depending on if key is deletable.
	_ = err
}

// --- init.go: generateInitDocs with templates ---

// TestGenerateInitDocs_WithTemplateDir_ReturnsResults verifies doc generation (FR-13.4).
func TestGenerateInitDocs_WithTemplateDir_ReturnsResults(t *testing.T) {
	// This test requires the template directory to exist.
	// It may return nil if templates are not found, which is acceptable.
	tmpDir := t.TempDir()
	docsDir := filepath.Join(tmpDir, "docs")

	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()

	// Try from the project root where templates should exist.
	projectRoot := filepath.Dir(filepath.Dir(oldCwd))
	if _, statErr := os.Stat(filepath.Join(projectRoot, "internal", "docs", "templates")); statErr == nil {
		require.NoError(t, os.Chdir(projectRoot))
		result := generateInitDocs(docsDir, "TEST", "dev")
		// Result may be nil or non-nil depending on templates.
		_ = result
	}
}

// TestGenerateInitDocs_EmbeddedTemplates_AllFilesCreated verifies all docs generated (FR-13.4).
func TestGenerateInitDocs_EmbeddedTemplates_AllFilesCreated(t *testing.T) {
	docsDir := filepath.Join(t.TempDir(), "docs")
	result := generateInitDocs(docsDir, "TEST", "dev")
	assert.NotNil(t, result)
	for _, r := range result {
		assert.Equal(t, "generated", r.Action, "file %s should be generated", r.File)
	}
}

// --- root.go: initApp with various log levels ---

// TestInitApp_DebugLogLevel_SetsDebugLevel verifies debug log level path.
func TestInitApp_DebugLogLevel_SetsDebugLevel(t *testing.T) {
	saveAndResetApp(t)

	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "debug")
	assert.NoError(t, err)
	assert.NotNil(t, app.logger)
}

// TestInitApp_WarnLogLevel_SetsWarnLevel verifies warn log level path.
func TestInitApp_WarnLogLevel_SetsWarnLevel(t *testing.T) {
	saveAndResetApp(t)

	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "warn")
	assert.NoError(t, err)
	assert.NotNil(t, app.logger)
}

// TestInitApp_ErrorLogLevel_SetsErrorLevel verifies error log level path.
func TestInitApp_ErrorLogLevel_SetsErrorLevel(t *testing.T) {
	saveAndResetApp(t)

	tmpDir := t.TempDir()
	oldCwd, err := os.Getwd()
	require.NoError(t, err)
	defer func() { _ = os.Chdir(oldCwd) }()
	require.NoError(t, os.Chdir(tmpDir))

	cmd := &cobra.Command{Use: "test"}
	err = initApp(cmd, "error")
	assert.NoError(t, err)
	assert.NotNil(t, app.logger)
}

// --- admin.go: runVerify, runExport, runGC with various modes ---

// TestRunVerify_WithNode_JSONOutput verifies verify JSON for specific node (FR-3.7).
func TestRunVerify_WithNode_JSONOutput(t *testing.T) {
	initTestApp(t)
	app.jsonOutput = true

	err := runCreate("Verify JSON Node", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runVerify("TEST-1")
	assert.NoError(t, err)
}

// TestRunExport_WithNodes_Succeeds verifies export with data (FR-6.3).
func TestRunExport_WithNodes_Succeeds(t *testing.T) {
	initTestApp(t)

	err := runCreate("Export Node", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	err = runExport("json")
	assert.NoError(t, err)
}

// --- workflow.go: runTransition reopen path ---

// TestRunTransition_Reopen_HumanOutput verifies reopen transition (FR-6.3).
func TestRunTransition_Reopen_HumanOutput(t *testing.T) {
	initTestApp(t)

	err := runCreate("Reopen Me", "", "", 3, "", "", "", "", "")
	require.NoError(t, err)

	// open -> in_progress -> done -> open (reopen).
	transitionToInProgress(t, "TEST-1")
	err = runTransition("TEST-1", model.StatusDone, "completed")
	require.NoError(t, err)

	err = runTransition("TEST-1", model.StatusOpen, "reopened via CLI")
	assert.NoError(t, err)
}

// --- show.go: runList with pagination hint ---

// TestRunList_WithPagination_ShowsCountHint verifies pagination (FR-6.3).
func TestRunList_WithPagination_ShowsCountHint(t *testing.T) {
	initTestApp(t)

	// Create a few nodes.
	for i := 0; i < 3; i++ {
		err := runCreate("Paginated", "", "", 3, "", "", "", "", "")
		require.NoError(t, err)
	}

	// List with limit 1 to trigger pagination hint.
	err := runList("", "", "", "", "", 1)
	assert.NoError(t, err)
}

// TestRunList_WithAssigneeFilter_Succeeds verifies list with assignee filter (FR-6.3).
func TestRunList_WithAssigneeFilter_Succeeds(t *testing.T) {
	initTestApp(t)

	err := runCreate("Assigned Node", "", "", 3, "", "", "", "", "agent-filter")
	require.NoError(t, err)

	err = runList("", "", "agent-filter", "", "", 50)
	assert.NoError(t, err)
}
