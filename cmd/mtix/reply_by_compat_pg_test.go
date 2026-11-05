// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// TestReplyBy_PostgresPull_054ReaderIngests runs the released-binary pull check
// against an isolated hub: a real 0.5.4 binary pulls an event carrying
// reply_by and ingests it (MTIX-112.20.3).
func TestReplyBy_PostgresPull_054ReaderIngests(t *testing.T) {
	dsn := requireCmdPG(t)
	dir, err := os.Getwd()
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), "go", "test", "./internal/store/sqlite", "-run", "^TestReplyBy_054BinarySyncsBoard$", "-count=1", "-v")
	cmd.Dir = filepath.Clean(filepath.Join(dir, "..", ".."))
	cmd.Env = append(hostguard.Environ(), "MTIX_REPLYBY_TEST_DSN="+dsn)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "released reply_by reader fixture: %s", output)
	require.Contains(t, string(output), "REPLY_BY_COMPAT_REAL_PG_PULL")
	t.Logf("released reply_by reader child output:\n%s", output)
}
