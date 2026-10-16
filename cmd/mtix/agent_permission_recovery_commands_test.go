// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reviewR1PendingCLI(t *testing.T) (string, *oplocal.State, oplocal.PermissionBatch) {
	t.Helper()
	root := doctorCLIBoard(t, "")
	denied := doctorCLIExecute(t, root, "", "agent", "deny", "held")
	require.Equal(t, 0, denied.exit, denied.stderr)
	state, _ := permissionDraftState(t)
	batch := oplocal.PermissionBatch{ID: "33333333333333333333333333333333", BoardID: permissionDraftIdentity(t, root), Mode: "file", Source: "synthetic interrupted file", Changes: []oplocal.PermissionChange{{AgentID: "new", Action: "allow"}}}
	prepare := func(_ context.Context, b oplocal.PermissionBatch) (oplocal.PermissionBatch, error) {
		b.NodeID = "TEST-1"
		b.Time = time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC)
		return b, nil
	}
	publish := func(context.Context, oplocal.PermissionBatch) error {
		return errors.New("synthetic unavailable notice")
	}
	require.Error(t, state.ApplyPermissionIntent(t.Context(), batch, prepare, publish))
	return root, state, batch
}

func TestPermissionRecoveryCLI_CancelledCaller_ListsAndPermanentlyRefusesRetry(t *testing.T) {
	root, state, batch := reviewR1PendingCLI(t)
	listed := doctorCLIExecute(t, root, "", "agent", "permissions", "recover", "--json")
	require.Equal(t, 0, listed.exit, listed.stderr)
	assert.Contains(t, listed.stdout, batch.ID)
	assert.Contains(t, listed.stdout, batch.Source)
	cancelled := doctorCLIExecute(t, root, "", "agent", "permissions", "recover", "--cancel", batch.ID)
	require.Equal(t, 0, cancelled.exit, cancelled.stderr)
	assert.Contains(t, cancelled.stdout, "cancelled with audit")
	pending, err := state.PendingPermissionBatches(t.Context())
	require.NoError(t, err)
	require.Empty(t, pending)
	grants, err := state.AgentGrants()
	require.NoError(t, err)
	require.Len(t, grants, 1)
	assert.Equal(t, "held", grants[0].AgentID)
	assert.True(t, grants[0].Denied)
	require.ErrorContains(t, state.ApplyPermissionIntent(t.Context(), batch, nil, nil), "cancelled or quarantined")
	rows, err := state.Confirmations(t.Context())
	require.NoError(t, err)
	require.Len(t, rows, 2)
	assert.Equal(t, "quarantined", rows[1].BoardLine)
}

func TestPermissionRecoveryCLI_GeneratedReference_MatchesActualHelp(t *testing.T) {
	actual, err := os.ReadFile(filepath.Join("..", "..", "docs", "CLI_REFERENCE.md"))
	require.NoError(t, err)
	cmd := newAgentPermissionRecoveryCmd()
	assert.Equal(t, cliReferenceSection(t, generatedHelpReference(t), cmd.Name(), cmd.Use), cliReferenceSection(t, string(actual), cmd.Name(), cmd.Use))
	root := newRootCmd()
	command, remaining, err := root.Find([]string{"agent", "permissions", "recover"})
	require.NoError(t, err)
	require.Empty(t, remaining)
	require.Equal(t, "recover", command.Name())
	require.NoError(t, command.ParseFlags([]string{"--cancel", "33333333333333333333333333333333"}))
}

func TestDoctorPermissions_PendingPublication_NamesRecoveryCommand(t *testing.T) {
	root, _, _ := reviewR1PendingCLI(t)
	output := doctorCLIExecute(t, root, "", "doctor", "--json")
	rows := doctorCLIRows(t, output.stdout)
	found := false
	for _, row := range rows {
		if row["id"] == "agents.confirmations" {
			found = true
			assert.Contains(t, row["fix"], "mtix agent permissions recover")
		}
	}
	require.True(t, found)
}

func reviewR1LegacyPendingCLI(t *testing.T) (string, *oplocal.State, oplocal.PermissionBatch) {
	t.Helper()
	root, state, batch := reviewR1PendingCLI(t)
	var pending map[string]json.RawMessage
	reports, err := state.ReadJSON("permissions-pending", &pending)
	require.NoError(t, err)
	require.Empty(t, reports)
	delete(pending, "before_agent_grants")
	delete(pending, "before_hash")
	require.NoError(t, state.WriteJSON("permissions-pending", pending))
	return root, state, batch
}

func TestDoctorPermissions_LegacyHold_PrintsExactOperatorClear(t *testing.T) {
	root, state, batch := reviewR1LegacyPendingCLI(t)
	plain := doctorCLIExecute(t, root, "", "agent", "permissions", "recover")
	require.Equal(t, 0, plain.exit, plain.stderr)
	assert.Contains(t, plain.stdout, "quarantine hold")
	assert.Contains(t, plain.stdout, "mtix agent allow new")
	listed := doctorCLIExecute(t, root, "", "agent", "permissions", "recover", "--json")
	require.Equal(t, 0, listed.exit, listed.stderr)
	var report struct {
		Pending []oplocal.PermissionBatch `json:"pending_batches"`
		Holds   []oplocal.AgentGrant      `json:"quarantine_holds"`
	}
	require.NoError(t, json.Unmarshal([]byte(listed.stdout), &report))
	require.Len(t, report.Pending, 1)
	require.Len(t, report.Holds, 1)
	assert.Equal(t, "new", report.Holds[0].AgentID)
	assert.True(t, report.Holds[0].Quarantined)
	assert.False(t, report.Holds[0].Denied)
	reviewR1DoctorHold(t, root)
	cancelled := doctorCLIExecute(t, root, "", "agent", "permissions", "recover", "--cancel", batch.ID)
	require.Equal(t, 0, cancelled.exit, cancelled.stderr)
	reviewR1DoctorHold(t, root)
	cleared := doctorCLIExecute(t, root, "", "agent", "allow", "new", "--yes")
	require.Equal(t, 0, cleared.exit, cleared.stderr)
	holds, err := state.QuarantinedPermissionGrants(t.Context())
	require.NoError(t, err)
	require.Empty(t, holds)
	require.ErrorContains(t, state.ApplyPermissionIntent(t.Context(), batch, nil, nil), "cancelled or quarantined")
}

func reviewR1DoctorHold(t *testing.T, root string) {
	t.Helper()
	output := doctorCLIExecute(t, root, "", "doctor", "--json")
	rows := doctorCLIRows(t, output.stdout)
	found := false
	for _, row := range rows {
		if row["id"] == "agents.quarantined" {
			found = true
			assert.Equal(t, "warn", row["status"])
			assert.Equal(t, "mtix agent allow new", row["fix"])
		}
		if row["id"] == "agents.denied" {
			assert.Equal(t, "info", row["status"])
			assert.Empty(t, row["fix"], "the unrelated explicit operator deny has no allow remedy")
		}
	}
	require.True(t, found)
}

func TestPermissionRecoveryCLI_EmptyOrInvalidCancellation_ReportsResult(t *testing.T) {
	root := doctorCLIBoard(t, "")
	empty := doctorCLIExecute(t, root, "", "agent", "permissions", "recover")
	require.Equal(t, 0, empty.exit, empty.stderr)
	assert.Contains(t, empty.stdout, "No pending")
	invalid := doctorCLIExecute(t, root, "", "agent", "permissions", "recover", "--cancel", "not-an-id")
	assert.NotEqual(t, 0, invalid.exit)
	assert.Contains(t, invalid.stderr, "identity is invalid")
}
