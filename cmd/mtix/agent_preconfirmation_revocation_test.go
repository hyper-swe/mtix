// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestDoctorAgents_PreconfirmationDenied_CurrentCountZero(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	allowed := doctorCLIExecute(t, root, "", "agent", "allow", "future-fixture", "--yes")
	require.Equal(t, 0, allowed.exit, allowed.stderr)
	denied := doctorCLIExecute(t, root, "", "agent", "deny", "future-fixture")
	require.Equal(t, 0, denied.exit, denied.stderr)
	output := doctorCLIExecute(t, root, "", "doctor", "--json")
	require.Equal(t, 0, output.exit, output.stderr)
	found := 0
	for _, row := range doctorCLIRows(t, output.stdout) {
		if row["id"] == "agents.confirmations" {
			found++
			assert.Contains(t, row["detail"], "pending registration count: 0")
			assert.NotContains(t, row["fix"], "mtix agent deny future-fixture")
		}
	}
	assert.Equal(t, 1, found)
	rows := permissionDraftLog(t)
	require.Len(t, rows, 2)
	assert.Equal(t, true, rows[0]["pending_registration"])
	assert.Equal(t, "deny", rows[1]["action"])
}
