// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// A valid permission request must not repair or overwrite unreadable grants.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestAgentPermissionsCobra_UnreadableGrantsCannotPublishOrOverwrite(t *testing.T) {
	root := doctorCLIBoard(t, "")
	id, err := service.CreateBoardID(filepath.Join(root, ".mtix"))
	require.NoError(t, err)
	state, _ := permissionDraftState(t)
	require.NoError(t, state.WriteJSON("grants", map[string]any{"agent_grants": "invalid"}))
	dir, err := state.Path()
	require.NoError(t, err)
	path := filepath.Join(dir, "grants.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	_, err = permissionCobraExecute(t, root, "agent", "allow", "future-agent", "--yes")
	require.Error(t, err)
	t.Logf("rejected permission request: %v", err)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, id, permissionDraftIdentity(t, root))
	for _, name := range []string{"confirmations.jsonl", "permissions-pending.json"} {
		_, err := os.Stat(filepath.Join(dir, name))
		require.ErrorIs(t, err, os.ErrNotExist)
	}
}
