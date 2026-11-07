// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify the initialization exclusion and its error handling.
package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunInit_DefaultExclusion_PreservesContent(t *testing.T) {
	saveAndResetApp(t)
	root := t.TempDir()
	t.Chdir(root)
	path := filepath.Join(root, ".gitignore")
	require.NoError(t, os.WriteFile(path, []byte("# project rules\nbuild/"), 0600))
	require.NoError(t, runInit("TEST"))
	content, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, "# project rules\nbuild/\n.mtix/hooks.trust\n.mtix/enroll-diff-*.json\n.mtix/enroll-lost-local-*.json\n.mtix/enrol-copy-*.db\n.mtix/serve.json\n.mtix/recovered-*.json\n", string(content))
}

func TestRunInit_ExclusionReadError_ReturnsError(t *testing.T) {
	saveAndResetApp(t)
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".gitignore"), 0700))
	assert.Error(t, runInit("TEST"))
}

// Private recovery artifacts cannot be staged by an ordinary git add (FR-19.1).
func TestRunInit_RecoveredMailIsGitIgnored(t *testing.T) {
	saveAndResetApp(t)
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, runInit("TEST"))
	cmd := exec.Command("git", "init", "--quiet", root)
	require.NoError(t, cmd.Run())
	recovered := filepath.Join(root, ".mtix", "recovered-20261106-010203.json")
	require.NoError(t, os.WriteFile(recovered, []byte(`{"private":"mail"}`), 0600))
	cmd = exec.Command("git", "check-ignore", "--", recovered)
	output, err := cmd.Output()
	require.NoError(t, err)
	require.Contains(t, string(output), "recovered-20261106-010203.json")
}
