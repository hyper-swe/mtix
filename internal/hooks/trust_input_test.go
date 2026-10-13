// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify approval input handling for configured exec hooks.
package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExecTrusted_SessionInput_ReturnsFalse(t *testing.T) {
	for _, marker := range []string{"CODEX_THREAD_ID", "CLAUDECODE", "PI_CODING_AGENT", "CODEX_SANDBOX"} {
		t.Run(marker, func(t *testing.T) {
			root := t.TempDir()
			dir := filepath.Join(root, ".mtix")
			require.NoError(t, os.Mkdir(dir, 0700))
			require.NoError(t, os.WriteFile(filepath.Join(root, "wake.sh"), []byte("#!/bin/sh\nexit 0\n"), 0700))
			config := "hooks:\n  - name: wake\n    match: {events: [comment.addressed]}\n    deliver: [exec]\n    exec: {command: [./wake.sh]}\n"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(config), 0600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)+"\n"), 0600))
			t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "operator"))
			t.Setenv("APPDATA", filepath.Join(root, "operator"))
			t.Setenv(marker, "test-session")
			require.False(t, ExecTrusted(dir))
		})
	}
}

func TestExecTrusted_NoExecHooks_ReturnsFalse(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte("hooks: []\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.trust"), []byte(ConfigHash(dir)+"\n"), 0600))
	require.False(t, ExecTrusted(dir))
}
