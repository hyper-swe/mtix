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
	require.Contains(t, long, "The REPLICATION role attribute is outside the check too: a role that has it is "+
		"checked for its privileges and memberships like any other role, but not for the attribute, so review the "+
		"roles that have it (rolreplication in pg_roles).")
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
