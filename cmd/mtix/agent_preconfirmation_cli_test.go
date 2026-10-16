// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestAgentAllow_PreconfirmBeforeRegister_LoggedPendingThenNoSecondConfirmation(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	out := doctorCLIExecute(t, root, "", "agent", "allow", "future-fixture", "--yes")
	require.Equal(t, 0, out.exit, out.stderr)
	rows := permissionDraftLog(t)
	require.Len(t, rows, 1)
	assert.Equal(t, "future-fixture", rows[0]["agent_id"])
	assert.Equal(t, true, rows[0]["pending_registration"])
	assert.Equal(t, permissionDraftIdentity(t, root), rows[0]["board_id"])
	beforeID := rows[0]["id"]
	diagnostic := doctorCLIExecute(t, root, "", "doctor", "--json")
	require.Equal(t, 0, diagnostic.exit, diagnostic.stderr)
	found := 0
	for _, row := range doctorCLIRows(t, diagnostic.stdout) {
		if row["id"] != "agents.confirmations" {
			continue
		}
		found++
		assert.Contains(t, row["detail"], "future-fixture")
		assert.Contains(t, row["detail"], "pending registration count: 1")
		assert.Contains(t, row["fix"]+row["detail"], "mtix agent deny future-fixture")
	}
	assert.Equal(t, 1, found)
	permissionDraftRegister(t, root, "future-fixture")
	after := doctorCLIExecute(t, root, "", "doctor", "--json")
	require.Equal(t, 0, after.exit, after.stderr)
	unconfirmed, confirmations := 0, 0
	for _, row := range doctorCLIRows(t, after.stdout) {
		if row["id"] == "agents.unconfirmed" {
			unconfirmed++
			assert.Equal(t, "pass", row["status"])
		}
		if row["id"] == "agents.confirmations" {
			confirmations++
			assert.Contains(t, row["detail"], "pending registration count: 0")
		}
	}
	assert.Equal(t, 1, unconfirmed)
	assert.Equal(t, 1, confirmations)
	retained := permissionDraftLog(t)
	require.Len(t, retained, 1, "registration must consume the existing confirmation without reconfirming")
	assert.Equal(t, beforeID, retained[0]["id"])
}

func TestAgentAllow_PreconfirmWithoutTTYOrYes_RefusedWithoutConfirmation(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	out := doctorCLIExecute(t, root, "", "agent", "allow", "future-fixture")
	assert.Equal(t, 1, out.exit)
	assert.Contains(t, out.stderr, "terminal")
	state, _ := permissionDraftState(t)
	rows, err := state.Confirmations(t.Context())
	require.NoError(t, err)
	assert.Empty(t, rows)
}

func TestAgentAllow_PreconfirmUnderHarness_AcceptsAndRecordsMarkerName(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	t.Setenv("CODEX_THREAD_ID", "synthetic-preconfirmation-marker-value")
	out := doctorCLIExecute(t, root, "", "agent", "allow", "future-fixture", "--yes")
	require.Equal(t, 0, out.exit, out.stderr)
	rows := permissionDraftLog(t)
	require.Len(t, rows, 1)
	assert.Equal(t, true, rows[0]["pending_registration"])
	assert.Equal(t, []any{"CODEX_THREAD_ID"}, rows[0]["harness_markers"])
	assert.Equal(t, "yes", rows[0]["mode"])
	encoded, err := json.Marshal(rows[0])
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "synthetic-preconfirmation-marker-value")
}
