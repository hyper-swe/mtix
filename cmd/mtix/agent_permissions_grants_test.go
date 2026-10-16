// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAgentAllow_GrantWrittenHostBoundAndExistingBoardGrantRetained(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	permissionDraftRegister(t, root, "permission-fixture")
	state, dir := permissionDraftState(t)
	prior := oplocal.Grant{BoardID: "11111111-1111-4111-8111-111111111111"}
	require.NoError(t, state.WriteJSON("grants", map[string]any{"board_grants": []oplocal.Grant{prior}}))
	out := doctorCLIExecute(t, root, "", "agent", "allow", "permission-fixture", "--yes")
	require.Equal(t, 0, out.exit, out.stderr)
	grants, reports, err := state.BoardGrants()
	require.NoError(t, err)
	assert.Empty(t, reports)
	assert.Equal(t, []oplocal.Grant{prior}, grants)
	var payload map[string]any
	reports, err = state.ReadJSON("grants", &payload)
	require.NoError(t, err)
	assert.Empty(t, reports)
	path := filepath.Join(dir, "grants.json")
	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	host, err := state.HostID()
	require.NoError(t, err)
	assert.Contains(t, string(data), host)
	assert.Contains(t, string(data), permissionDraftIdentity(t, root))
	assert.Contains(t, string(data), "permission-fixture")
}

func TestAgentAllowYes_HarnessPrefixes_AcceptedAndLogNamesOnly(t *testing.T) {
	for _, marker := range []string{"CLAUDECODE", "CLAUDE_CODE_FIXTURE", "CODEX_THREAD_ID", "CODEX_SANDBOX_FIXTURE", "PI_FIXTURE"} {
		t.Run(marker, func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			permissionDraftClearMarkers(t)
			t.Setenv(marker, "synthetic-marker-value")
			permissionDraftRegister(t, root, "permission-fixture")
			out := doctorCLIExecute(t, root, "", "agent", "allow", "permission-fixture", "--yes")
			require.Equal(t, 0, out.exit, out.stderr)
			rows := permissionDraftLog(t)
			require.Len(t, rows, 1)
			assert.Equal(t, []any{marker}, rows[0]["harness_markers"])
			encoded, err := json.Marshal(rows[0])
			require.NoError(t, err)
			assert.NotContains(t, string(encoded), "synthetic-marker-value")
		})
	}
}
