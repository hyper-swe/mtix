// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/require"
)

func permissionCobraExecute(t *testing.T, project string, args ...string) (string, error) {
	t.Helper()
	previous, err := os.Getwd()
	require.NoError(t, err)
	defer func() { require.NoError(t, os.Chdir(previous)) }()
	saveAndResetApp(t)
	defer func() { require.NoError(t, closeApp()) }()
	command := newRootCmd()
	var output bytes.Buffer
	command.SetOut(&output)
	command.SetErr(&output)
	command.SetArgs(append([]string{"-C", project}, args...))
	err = command.ExecuteContext(t.Context())
	return output.String(), err
}

func permissionCobraPipe(t *testing.T) {
	t.Helper()
	read, write, err := os.Pipe()
	require.NoError(t, err)
	require.NoError(t, write.Close())
	previous := os.Stdin
	os.Stdin = read
	t.Cleanup(func() { os.Stdin = previous; require.NoError(t, read.Close()) })
}

func permissionCobraRows(t *testing.T) []oplocal.Confirmation {
	t.Helper()
	state, _ := permissionDraftState(t)
	rows, err := state.Confirmations(t.Context())
	require.NoError(t, err)
	return rows
}

func TestAgentPermissionsCobra_ActualYesAndDenyRetainAuditPrivacy(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	permissionCobraPipe(t)
	t.Setenv("CODEX_THREAD_ID", "synthetic-cobra-private-value")
	_, err := permissionCobraExecute(t, root, "agent", "allow", "future-agent", "--yes")
	require.NoError(t, err)
	id := permissionDraftIdentity(t, root)
	rows := permissionCobraRows(t)
	require.Len(t, rows, 1)
	require.True(t, rows[0].PendingRegistration)
	require.Equal(t, "yes", rows[0].Mode)
	require.Equal(t, "mtix agent allow", rows[0].Source)
	require.Equal(t, []string{"CODEX_THREAD_ID"}, rows[0].Markers)
	encoded, err := json.Marshal(rows)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "synthetic-cobra-private-value")
	_, err = permissionCobraExecute(t, root, "agent", "deny", "future-agent")
	require.NoError(t, err)
	require.Equal(t, id, permissionDraftIdentity(t, root))
	rows = permissionCobraRows(t)
	require.Len(t, rows, 2)
	require.Equal(t, "deny", rows[1].Action)
	require.False(t, rows[1].PendingRegistration)
	state, _ := permissionDraftState(t)
	grants, err := state.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	require.True(t, grants[0].Denied)
}

func TestAgentPermissionsCobra_InvalidIDAndTTYRefusalDoNotMintOrGrant(t *testing.T) {
	for _, args := range [][]string{{"agent", "allow", strings.Repeat("x", 65), "--yes"}, {"agent", "allow", "future-agent"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			permissionDraftClearMarkers(t)
			permissionCobraPipe(t)
			_, err := permissionCobraExecute(t, root, args...)
			require.Error(t, err)
			_, err = os.Stat(filepath.Join(root, ".mtix", "board-id"))
			require.ErrorIs(t, err, os.ErrNotExist)
			require.Empty(t, permissionCobraRows(t))
			state, _ := permissionDraftState(t)
			grants, err := state.AgentGrants()
			require.NoError(t, err)
			require.Empty(t, grants)
		})
	}
}

func TestAgentPermissionsCobra_InvalidBoardIdentityPreservesOriginalBytes(t *testing.T) {
	for _, contents := range []string{"not-a-board-id\n", "11111111-1111-4111-8111-111111111111\n22222222-2222-4222-8222-222222222222\n"} {
		t.Run(contents[:8], func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			path := filepath.Join(root, ".mtix", "board-id")
			require.NoError(t, os.WriteFile(path, []byte(contents), 0600))
			_, err := permissionCobraExecute(t, root, "agent", "allow", "future-agent", "--yes")
			require.Error(t, err)
			after, err := os.ReadFile(path)
			require.NoError(t, err)
			require.Equal(t, contents, string(after))
			require.Empty(t, permissionCobraRows(t))
		})
	}
}
