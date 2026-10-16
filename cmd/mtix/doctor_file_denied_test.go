// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func doctorDeniedFileFixture(t *testing.T) (*service.WakePermissions, service.PermissionBoard) {
	t.Helper()
	initTestApp(t)
	permissionDraftClearMarkers(t)
	require.NoError(t, runAgentRegister("fixture-agent", sessionOptions{}))
	svc, board, err := configuredPermissions()
	require.NoError(t, err)
	if board.ID == "" {
		board.ID, err = service.CreateBoardID(app.mtixDir)
		require.NoError(t, err)
	}
	require.NoError(t, svc.ApplyBatch(t.Context(), board, oplocal.PermissionBatch{Mode: "yes", Source: "mtix agent deny", Changes: []oplocal.PermissionChange{{AgentID: "fixture-agent", Action: "deny"}}}))
	require.NoError(t, svc.ApplyBatch(t.Context(), board, oplocal.PermissionBatch{ID: "0123456789abcdef0123456789abcdef", Mode: "file", Source: "fixture file", Changes: []oplocal.PermissionChange{{AgentID: "fixture-agent", Action: "allow"}}}))
	return svc, board
}

func TestDoctorAgents_FileDeniedNoOp_ReportsCountWithoutAllowRemedy(t *testing.T) {
	svc, _ := doctorDeniedFileFixture(t)
	before, err := svc.State.Confirmations(t.Context())
	require.NoError(t, err)
	out := doctorCLIExecute(t, filepath.Dir(app.mtixDir), "", "doctor", "--json", "--fix")
	require.Equal(t, 0, out.exit, out.stderr)
	found := 0
	for _, row := range doctorCLIRows(t, out.stdout) {
		if row["id"] == "agents.confirmations" {
			found++
			assert.Contains(t, row["detail"], "denied_requires_operator")
			assert.Contains(t, row["detail"], "denied file allow count: 1")
			assert.NotContains(t, out.stdout+out.stderr, "mtix agent allow fixture-agent")
			assert.Equal(t, "info", row["status"])
		}
	}
	assert.Equal(t, 1, found)
	after, err := svc.State.Confirmations(t.Context())
	require.NoError(t, err)
	assert.Equal(t, before, after, "doctor --fix neither grants nor rewrites outcomes")
}

func TestAgentAllowYes_ActualOperatorCommand_ClearsFileProtectedDenyWithMarkers(t *testing.T) {
	svc, board := doctorDeniedFileFixture(t)
	denied := doctorCLIExecute(t, filepath.Dir(app.mtixDir), "", "agent", "deny", "fixture-agent")
	require.Equal(t, 0, denied.exit, denied.stderr)
	held, reason, err := svc.Wakeable(t.Context(), board, "fixture-agent")
	require.NoError(t, err)
	require.False(t, held)
	require.Equal(t, "denied", reason)
	t.Setenv("CODEX_THREAD_ID", "synthetic7442-private-value")
	out := doctorCLIExecute(t, filepath.Dir(app.mtixDir), "", "agent", "allow", "fixture-agent", "--yes")
	require.Equal(t, 0, out.exit, out.stderr)
	ok, _, err := svc.Wakeable(t.Context(), board, "fixture-agent")
	require.NoError(t, err)
	assert.True(t, ok)
	rows, err := svc.State.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 4)
	assert.Equal(t, "allow", rows[3].Action)
	assert.Equal(t, "yes", rows[3].Mode)
	assert.Equal(t, "mtix agent allow", rows[3].Source)
	assert.Equal(t, []string{"CODEX_THREAD_ID"}, rows[3].Markers)
}
