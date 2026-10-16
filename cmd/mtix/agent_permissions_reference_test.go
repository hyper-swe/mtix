// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentPermissions_CLIReferenceMatchesActualGenerator(t *testing.T) {
	actual, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	generated := generatedHelpReference(t)
	for _, action := range []string{"allow", "deny"} {
		command := newAgentPermissionCmd(action)
		require.Equal(t, cliReferenceSection(t, generated, action, command.Use), cliReferenceSection(t, string(actual), action, command.Use))
	}
}
