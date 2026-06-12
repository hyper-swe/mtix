// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// CLI tests for the Codex and pi plugin targets (MTIX-27, issue #15).
// Written RED-first per TDD-WORKFLOW.md §1.1.
package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// captureRunPluginInstall runs runPluginInstall capturing stdout.
func captureRunPluginInstall(t *testing.T, target string) string {
	t.Helper()
	oldStdout := os.Stdout
	r, w, err := os.Pipe()
	require.NoError(t, err)
	os.Stdout = w

	runErr := runPluginInstall(target, false)

	_ = w.Close()
	os.Stdout = oldStdout
	out, _ := io.ReadAll(r)
	require.NoError(t, runErr)
	return string(out)
}

// TestRunPluginInstall_Codex_EndToEnd: the codex target produces
// AGENTS.md and the MCP config in the working directory.
func TestRunPluginInstall_Codex_EndToEnd(t *testing.T) {
	initTestApp(t)

	out := captureRunPluginInstall(t, "codex")
	assert.Contains(t, out, "AGENTS.md")
	assert.Contains(t, out, "config.toml")

	cwd, err := os.Getwd()
	require.NoError(t, err)
	assert.FileExists(t, filepath.Join(cwd, "AGENTS.md"))
	assert.FileExists(t, filepath.Join(cwd, ".codex", "config.toml"))
}

// TestRunPluginInstall_Pi_PrintsAdapterGuidance: manual-action notes must
// reach the user — guidance nobody sees is the MTIX-18 docs failure mode
// in a different costume.
func TestRunPluginInstall_Pi_PrintsAdapterGuidance(t *testing.T) {
	initTestApp(t)

	out := captureRunPluginInstall(t, "pi")
	assert.Contains(t, out, "pi-mcp-adapter",
		"the printed output must include the manual MCP guidance")
	assert.Contains(t, out, "mtix mcp")
}

// TestPluginInstallCmd_HelpListsRealTargets: the flag help advertises
// exactly the implemented targets.
func TestPluginInstallCmd_HelpListsRealTargets(t *testing.T) {
	cmd := newPluginInstallCmd()
	usage := cmd.Flag("target").Usage
	for _, want := range []string{"claude-code", "codex", "pi"} {
		assert.Contains(t, usage, want)
	}
	assert.NotContains(t, usage, "windsurf",
		"windsurf was never implemented; the help must not advertise it")
}
