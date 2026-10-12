// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify startup approval migration and legacy input selection.
package main

import (
	"crypto/sha1"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func TestInitApp_EligibleLegacyInput_CreatesOperatorRecord(t *testing.T) {
	saveAndResetApp(t)
	root := startupApprovalFixture(t, false)
	t.Chdir(root)
	require.NoError(t, initApp(&cobra.Command{Use: "show"}, "error"))
	t.Cleanup(func() { require.NoError(t, app.store.Close()) })
	env, err := oplocal.FromProcess()
	require.NoError(t, err)
	status, err := oplocal.New(env).Inspect()
	require.NoError(t, err)
	assert.Equal(t, "ready", status.Status)
	assert.True(t, hooks.ExecTrusted(app.mtixDir))
}

func TestExecTrusted_LegacyInputSelection_ReportsDecision(t *testing.T) {
	for _, tc := range []struct {
		name    string
		tracked bool
		changed bool
	}{
		{name: "tracked", tracked: true},
		{name: "changed", changed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := startupApprovalFixture(t, tc.tracked)
			dir := filepath.Join(root, ".mtix")
			if tc.changed {
				require.NoError(t, os.WriteFile(filepath.Join(root, "wake.sh"), []byte("#!/bin/sh\nexit 1\n"), 0700))
			}
			assert.False(t, hooks.ExecTrusted(dir))
		})
	}
}

func startupApprovalFixture(t *testing.T, tracked bool) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(operatorTestHome(t))
	require.NoError(t, err)
	if os.PathSeparator == '\\' {
		t.Setenv("USERPROFILE", root)
	} else {
		t.Setenv("HOME", root)
	}
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "operator"))
	t.Setenv("APPDATA", filepath.Join(root, "operator"))
	for _, key := range []string{"CODEX_THREAD_ID", "CODEX_SANDBOX", "CODEX_SANDBOX_NETWORK_DISABLED", "CLAUDECODE", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_ENV_FILE", "PI_SESSION_ID", "PI_SESSION_FILE", "PI_CODING_AGENT", "CODEX_HOME"} {
		t.Setenv(key, "")
	}
	project := filepath.Join(root, "project")
	dir := filepath.Join(project, ".mtix")
	require.NoError(t, os.MkdirAll(dir, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("prefix: TEST\nmax_depth: 10\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(project, "wake.sh"), []byte("#!/bin/sh\nexit 0\n"), 0700))
	config := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [./wake.sh]}\n"
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(config), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(hooks.ConfigHash(dir)+"\n"), 0600))
	writeStartupIndex(t, project, tracked)
	return project
}

func writeStartupIndex(t *testing.T, root string, tracked bool) {
	t.Helper()
	gitdir := filepath.Join(root, ".git")
	require.NoError(t, os.Mkdir(gitdir, 0700))
	data := make([]byte, 12)
	copy(data, "DIRC")
	binary.BigEndian.PutUint32(data[4:8], 2)
	if tracked {
		binary.BigEndian.PutUint32(data[8:12], 1)
		entry := make([]byte, 62)
		name := ".mtix/hooks.trust"
		binary.BigEndian.PutUint32(entry[24:28], 0100644)
		binary.BigEndian.PutUint16(entry[60:62], uint16(len(name)))
		entry = append(entry, []byte(name)...)
		entry = append(entry, 0)
		for len(entry)%8 != 0 {
			entry = append(entry, 0)
		}
		data = append(data, entry...)
	}
	checksum := sha1.Sum(data) //nolint:gosec // Git index fixtures use the format's SHA-1 checksum.
	data = append(data, checksum[:]...)
	require.NoError(t, os.WriteFile(filepath.Join(gitdir, "index"), data, 0600))
}
