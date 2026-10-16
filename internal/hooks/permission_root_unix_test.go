//go:build !windows

// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Metadata traversal permission must not imply a readable instruction root.
package hooks

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInstructionAgents_ExecuteOnlyRootCannotReturnPartialNames(t *testing.T) {
	root := instructionCoverageRoot(t, "AGENTS.md")
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("mtix agent register fixture-agent\n"), 0600))
	require.NoError(t, os.Chmod(root, 0111))
	t.Cleanup(func() { require.NoError(t, os.Chmod(root, 0700)) })
	ids, err := InstructionAgents(root, nil)
	require.Error(t, err)
	require.Empty(t, ids)
}
