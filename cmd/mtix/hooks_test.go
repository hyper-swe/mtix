// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeHooksYAML seeds a temp .mtix dir with hooks.yaml and returns the dir.
// captureStdout (projects_scope_test.go) and captureStderr (create_test.go) are
// reused for output assertions.
func writeHooksYAML(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(body), 0o600))
	return dir
}

const sampleHooks = `
hooks:
  - name: wake-worker
    match:
      events: [status.changed]
      status-to: [done]
    deliver: [inbox]
  - name: log-creates
    match:
      events: [node.created]
    deliver: [append-file]
    append-file:
      path: creates.log
`

// TestRunHooksList_NoProject_ReturnsError verifies the mtixDir guard.
func TestRunHooksList_NoProject_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runHooksList()
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunHooksList_PrintsHookNames verifies list prints each hook's name,
// events, and delivery adapters.
func TestRunHooksList_PrintsHookNames(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{mtixDir: writeHooksYAML(t, sampleHooks)}

	out := captureStdout(t, func() {
		require.NoError(t, runHooksList())
	})
	assert.Contains(t, out, "wake-worker")
	assert.Contains(t, out, "log-creates")
	assert.Contains(t, out, "status.changed")
	assert.Contains(t, out, "inbox")
	assert.Contains(t, out, "append-file")
}

// TestRunHooksList_Empty_HumanMessage verifies the empty/missing case.
func TestRunHooksList_Empty_HumanMessage(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{mtixDir: t.TempDir()} // no hooks.yaml → empty config

	out := captureStdout(t, func() {
		require.NoError(t, runHooksList())
	})
	assert.Contains(t, out, "(no hooks configured)")
}

// TestRunHooksList_JSONEmpty_PrintsArray verifies json empty case is [].
func TestRunHooksList_JSONEmpty_PrintsArray(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{mtixDir: t.TempDir(), jsonOutput: true}

	out := captureStdout(t, func() {
		require.NoError(t, runHooksList())
	})
	assert.Contains(t, out, "[]")
}

// TestRunHooksFire_NoProject_ReturnsError verifies the mtixDir guard.
func TestRunHooksFire_NoProject_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{}

	err := runHooksFire("status.changed", "", "", "", "done", false, true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "not in an mtix project")
}

// TestRunHooksFire_UnknownEvent_ReturnsError verifies event-name validation.
func TestRunHooksFire_UnknownEvent_ReturnsError(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{mtixDir: writeHooksYAML(t, sampleHooks)}

	err := runHooksFire("not.a.real.event", "", "", "", "", false, true)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unknown event")
}

// TestRunHooksFire_DryRunMatching_ListsHook verifies a matching event lists the hook.
func TestRunHooksFire_DryRunMatching_ListsHook(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{mtixDir: writeHooksYAML(t, sampleHooks)}

	out := captureStdout(t, func() {
		require.NoError(t, runHooksFire("status.changed", "", "", "", "done", false, true))
	})
	assert.Contains(t, out, "wake-worker")
	assert.Contains(t, out, "inbox")
	assert.NotContains(t, out, "log-creates")
}

// TestRunHooksFire_DryRunNonMatching_ListsNone verifies a non-matching event
// lists no hooks.
func TestRunHooksFire_DryRunNonMatching_ListsNone(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{mtixDir: writeHooksYAML(t, sampleHooks)}

	out := captureStdout(t, func() {
		// status.changed but status-to=open does not satisfy the [done] filter.
		require.NoError(t, runHooksFire("status.changed", "", "", "", "open", false, true))
	})
	assert.Contains(t, out, "(no hooks match this event)")
	assert.NotContains(t, out, "wake-worker")
}

// TestRunHooksFire_WithoutDryRun_ReturnsNilNotesLimitation verifies the
// dispatcher-not-yet gate: no --dry-run is a no-op that returns nil and notes
// the limitation on stderr.
func TestRunHooksFire_WithoutDryRun_ReturnsNilNotesLimitation(t *testing.T) {
	old := app
	defer func() { app = old }()
	app = appContext{mtixDir: writeHooksYAML(t, sampleHooks)}

	stderr := captureStderr(t, func() {
		err := runHooksFire("status.changed", "", "", "", "done", false, false)
		require.NoError(t, err)
	})
	assert.Contains(t, stderr, "--dry-run")
}
