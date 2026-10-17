// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

func permissionCobraDoctorRow(t *testing.T, text, id string) map[string]string {
	t.Helper()
	var found []map[string]string
	for _, row := range doctorCLIRows(t, text) {
		if row["id"] == id {
			found = append(found, row)
		}
	}
	require.Len(t, found, 1)
	return found[0]
}

func TestDoctorPermissionsCobra_RegistrationActivatesPendingWithoutSecondConfirmation(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	_, err := permissionCobraExecute(t, root, "agent", "allow", "future-agent", "--yes")
	require.NoError(t, err)
	before := permissionCobraRows(t)
	require.Len(t, before, 1)
	text, err := permissionCobraExecute(t, root, "doctor", "--json", "--fix")
	require.NoError(t, err)
	row := permissionCobraDoctorRow(t, text, "agents.confirmations")
	require.Contains(t, row["detail"], "pending registration count: 1")
	require.Contains(t, row["fix"], "mtix agent deny future-agent")
	require.Equal(t, before, permissionCobraRows(t))
	_, err = permissionCobraExecute(t, root, "agent", "register", "future-agent", "--harness", "fixture", "--session", "fixture-session")
	require.NoError(t, err)
	text, err = permissionCobraExecute(t, root, "doctor", "--json", "--fix")
	require.NoError(t, err)
	row = permissionCobraDoctorRow(t, text, "agents.confirmations")
	require.Contains(t, row["detail"], "pending registration count: 0")
	require.Equal(t, string(service.DoctorPass), permissionCobraDoctorRow(t, text, "agents.unconfirmed")["status"])
	require.Equal(t, before, permissionCobraRows(t))
}

func TestDoctorPermissionsCobra_CorruptProtectedStateFailsWithoutRepair(t *testing.T) {
	root := doctorCLIBoard(t, "")
	_, err := permissionCobraExecute(t, root, "agent", "allow", "future-agent", "--yes")
	require.NoError(t, err)
	state, dir := permissionDraftState(t)
	require.NoError(t, state.WriteJSON("grants", map[string]any{"agent_grants": "corrupt"}))
	path := filepath.Join(dir, "grants.json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	text, err := permissionCobraExecute(t, root, "doctor", "--json", "--fix")
	require.Equal(t, 2, exitCodeForError(err))
	row := permissionCobraDoctorRow(t, text, "agents.confirmations")
	require.Equal(t, string(service.DoctorFail), row["status"])
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
}

func TestAgentPermissionsCobra_NoProjectCannotRecordPermission(t *testing.T) {
	doctorCLIBoard(t, "")
	_, err := permissionCobraExecute(t, t.TempDir(), "agent", "allow", "future-agent", "--yes")
	require.ErrorContains(t, err, "not in an mtix project")
	require.Empty(t, permissionCobraRows(t))
}
