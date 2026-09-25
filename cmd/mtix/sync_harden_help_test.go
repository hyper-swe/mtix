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

// TestSyncHardenCmd_Help_StatesWhatTheCheckCovers: the help says what the
// check covers: superusers are not checked, and the REPLICATION role
// attribute is outside the check, with the advice to review the roles that
// have it (MTIX-95.1.4).
func TestSyncHardenCmd_Help_StatesWhatTheCheckCovers(t *testing.T) {
	long := strings.Join(strings.Fields(newSyncHardenCmd().Long), " ")
	require.Contains(t, long, "Superusers are not checked.")
	require.Contains(t, long, "The REPLICATION role attribute is outside the check")
	require.Contains(t, long, "review the roles that have it")
}

// TestSyncHardenCmd_CLIReference_MatchesHelp: docs/CLI_REFERENCE.md
// carries the harden command's help exactly as the command prints it
// (MTIX-95.1.4).
func TestSyncHardenCmd_CLIReference_MatchesHelp(t *testing.T) {
	cmd := newSyncHardenCmd()
	ref, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	section := "## harden\n\n**Usage:** `harden`\n\n" + cmd.Short + "\n\n" + cmd.Long + "\n"
	require.Contains(t, string(ref), section, "docs/CLI_REFERENCE.md matches mtix sync harden --help")
}
