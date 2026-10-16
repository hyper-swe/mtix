// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Help and generated reference expose the actual count command contract.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboxCountHelp_ReferenceMatchesCommandTree(t *testing.T) {
	cmd := newInboxCmd()
	require.Contains(t, cmd.Long, "--count --all")
	require.Contains(t, cmd.Long, "--wait=false")
	actual, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	generated := generatedHelpReference(t)
	require.Equal(t, cliReferenceSection(t, generated, cmd.Name(), cmd.Use), cliReferenceSection(t, string(actual), cmd.Name(), cmd.Use))
}
