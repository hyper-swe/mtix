// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/stretchr/testify/require"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestHopField_PostgresPushPull_054ReaderIgnores(t *testing.T) {
	dsn := requireCmdPG(t)
	dir, err := os.Getwd()
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), "go", "test", "./internal/store/sqlite", "-run", "^TestHopField_PushPull_054ReaderIgnores$", "-count=1", "-v")
	cmd.Dir = filepath.Clean(filepath.Join(dir, "..", ".."))
	cmd.Env = append(os.Environ(), "MTIX_HOP_TEST_DSN="+dsn)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "released hop reader fixture: %s", output)
	require.Contains(t, string(output), "HOP_COMPAT_REAL_PG_PUSH_PULL")
	t.Logf("released hop reader child pid=%d output:\n%s", cmd.Process.Pid, output)
}
