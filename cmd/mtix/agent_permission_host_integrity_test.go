// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// A damaged operator host identity must not be replaced by a permission command.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentPermissionsCobra_DamagedHostIdentityRefusesBeforeGrantOrMint(t *testing.T) {
	root := doctorCLIBoard(t, "")
	state, _ := permissionDraftState(t)
	_, err := state.HostID()
	require.NoError(t, err)
	dir, err := state.Path()
	require.NoError(t, err)
	path := filepath.Join(dir, "host-id")
	broken := []byte("invalid-synthetic-host\n")
	require.NoError(t, os.WriteFile(path, broken, 0600))
	_, err = permissionCobraExecute(t, root, "agent", "allow", "future-agent", "--yes")
	require.Error(t, err)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, broken, after)
	_, err = os.Stat(filepath.Join(root, ".mtix", "board-id"))
	require.ErrorIs(t, err, os.ErrNotExist)
	for _, name := range []string{"grants.json", "confirmations.jsonl", "permissions-pending.json"} {
		_, err = os.Stat(filepath.Join(dir, name))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}
