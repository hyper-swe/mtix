// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// The published sync-init reference must expose the real command flags (FR-13.2).
func TestSyncInit_CLIReferenceMatchesCommandTree(t *testing.T) {
	cmd := newSyncInitCmd()
	actual, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	// Include Short to distinguish sync init from the root init command.
	marker := "## init\n\n**Usage:** `init`\n\n" + cmd.Short + "\n"
	section := func(reference string) string {
		start := strings.Index(reference, marker)
		require.NotEqual(t, -1, start)
		tail := reference[start:]
		end := strings.Index(tail, "\n---\n")
		require.NotEqual(t, -1, end)
		return tail[:end]
	}
	require.Equal(t, section(generatedHelpReference(t)), section(string(actual)))
}
