// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestOutputWriter_HumanRendering exercises the human writer's WriteHuman and
// WriteTable (used by nearly every command's non-JSON path).
func TestOutputWriter_HumanRendering(t *testing.T) {
	saveAndResetApp(t)
	app.jsonOutput = false
	out := captureStdout(t, func() {
		w := NewOutputWriter(false)
		w.WriteHuman("hello %s\n", "world")
		w.WriteTable([]string{"A", "B"}, [][]string{{"1", "2"}, {"3", "4"}})
	})
	assert.Contains(t, out, "hello world")
	assert.Contains(t, out, "A")
	assert.Contains(t, out, "3")
}

// TestRunHooksLog_RendersFirings: 'mtix hooks log' reads the audit log and
// renders it (and reports the empty case).
func TestRunHooksLog_RendersFirings(t *testing.T) {
	initTestApp(t)

	empty := captureStdout(t, func() { require.NoError(t, runHooksLog(50)) })
	assert.Contains(t, empty, "no hook firings", "empty log message")

	require.NoError(t, app.store.WriteHookLog(context.Background(), sqlite.HookLogEntry{
		Hook: "wake-dev", NodeID: "TEST-1", Event: "comment.addressed", Adapter: "exec", Outcome: "delivered",
	}))
	out := captureStdout(t, func() { require.NoError(t, runHooksLog(50)) })
	assert.Contains(t, out, "wake-dev")
	assert.Contains(t, out, "delivered")
}

// TestRunHooksTrust_PinsAndReports: 'mtix hooks trust' pins the current
// hooks.yaml hash; --status then reports it trusted.
func TestRunHooksTrust_PinsAndReports(t *testing.T) {
	initTestApp(t)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(`
hooks:
  - name: wake
    match: { events: [comment.addressed], to-agent: dev }
    deliver: [exec]
    exec: { command: ["/bin/true"] }
`), 0o600))

	assert.False(t, hooks.ExecTrusted(app.mtixDir), "untrusted before")
	out := captureStdout(t, func() { require.NoError(t, runHooksTrust(false)) })
	assert.Contains(t, out, "trusted")
	assert.True(t, hooks.ExecTrusted(app.mtixDir), "trusted after")

	status := captureStdout(t, func() { require.NoError(t, runHooksTrust(true)) })
	assert.Contains(t, status, "trusted")
}
