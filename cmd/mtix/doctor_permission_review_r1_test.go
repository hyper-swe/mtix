// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reviewR1DoctorFailureRows(t *testing.T, out doctorCLIOutput) {
	t.Helper()
	assert.Contains(t, []int{0, 2}, out.exit, out.stderr)
	rows := doctorCLIRows(t, out.stdout)
	permission, unrelated := 0, 0
	for _, row := range rows {
		if strings.HasPrefix(row["id"], "agents.") {
			permission++
			assert.Contains(t, []string{"warn", "fail"}, row["status"])
			assert.NotEmpty(t, row["fix"])
		} else {
			unrelated++
		}
	}
	assert.GreaterOrEqual(t, permission, 2)
	assert.Greater(t, unrelated, 0, "permission failures must retain other health checks")
}

func TestReviewR1_DoctorConflictedBoardIdentityKeepsReport(t *testing.T) {
	root := doctorCLIBoard(t, filepath.Join(t.TempDir(), "unavailable-relay"))
	ids := "11111111-1111-4111-8111-111111111111\n22222222-2222-4222-8222-222222222222\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "board-id"), []byte(ids), 0600))
	out := doctorCLIExecute(t, root, "", "doctor", "--json")
	reviewR1DoctorFailureRows(t, out)
}

func TestReviewR1_DoctorUnusableStateKeepsReport(t *testing.T) {
	root := doctorCLIBoard(t, filepath.Join(t.TempDir(), "unavailable-relay"))
	_, dir := permissionDraftState(t)
	require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0700))
	require.NoError(t, os.WriteFile(dir, []byte("synthetic non-directory state"), 0600))
	out := doctorCLIExecute(t, root, "", "doctor", "--json")
	reviewR1DoctorFailureRows(t, out)
	data, err := os.ReadFile(dir)
	require.NoError(t, err)
	assert.Equal(t, "synthetic non-directory state", string(data))
}

func TestReviewR1_DoctorCreatesNoOperatorState(t *testing.T) {
	root := doctorCLIBoard(t, "")
	_, dir := permissionDraftState(t)
	_, err := os.Stat(dir)
	require.True(t, os.IsNotExist(err))
	out := doctorCLIExecute(t, root, "", "doctor", "--json")
	assert.Equal(t, 0, out.exit, out.stderr)
	doctorCLIRows(t, out.stdout)
	_, err = os.Stat(dir)
	assert.True(t, os.IsNotExist(err), "a diagnostic must not create any operator state")
}

func TestReviewR1_DoctorDeniedDecisionHasNoAllowRemedy(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	permissionDraftRegister(t, root, "review-denied")
	denied := doctorCLIExecute(t, root, "", "agent", "deny", "review-denied")
	require.Equal(t, 0, denied.exit, denied.stderr)
	out := doctorCLIExecute(t, root, "", "doctor", "--json")
	assert.Equal(t, 0, out.exit, out.stderr)
	assert.NotContains(t, out.stdout+out.stderr, "mtix agent allow review-denied")
	found := false
	for _, row := range doctorCLIRows(t, out.stdout) {
		if row["status"] == "info" && strings.Contains(row["detail"], "denied by operator") {
			found = true
			assert.Contains(t, row["detail"], "review-denied")
			assert.Contains(t, row["detail"], "mtix agent deny")
			assert.Empty(t, row["fix"])
		}
	}
	assert.True(t, found)
}
