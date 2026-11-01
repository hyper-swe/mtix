// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify the initialization exclusion and its error handling.
package main

import (
	"os"
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
	assert.Equal(t, "# project rules\nbuild/\n.mtix/hooks.trust\n.mtix/serve.json\n", string(content))
}

func TestRunInit_ExclusionReadError_ReturnsError(t *testing.T) {
	saveAndResetApp(t)
	root := t.TempDir()
	t.Chdir(root)
	require.NoError(t, os.Mkdir(filepath.Join(root, ".gitignore"), 0700))
	assert.Error(t, runInit("TEST"))
}
