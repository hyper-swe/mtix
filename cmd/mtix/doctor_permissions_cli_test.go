// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func permissionDraftMarkHub(t *testing.T, root string) {
	t.Helper()
	store, err := sqlite.New(filepath.Join(root, ".mtix", "data", "mtix.db"), nil)
	require.NoError(t, err)
	flipped, markErr := store.MarkHubConnected(context.Background(), "foreign_event")
	closeErr := store.Close()
	require.NoError(t, markErr)
	require.NoError(t, closeErr)
	require.True(t, flipped, "a real sticky mark must transition before diagnosing unconfirmed ids")
}

func permissionDraftAssertConfirmationRow(t *testing.T, body string, record map[string]any) {
	t.Helper()
	rows := doctorCLIRows(t, body)
	found := 0
	for _, row := range rows {
		if row["id"] != "agents.confirmations" {
			continue
		}
		found++
		assert.Equal(t, "info", row["status"])
		for _, field := range []string{"time", "source", "action", "mode", "board_line"} {
			value, ok := record[field].(string)
			require.True(t, ok)
			assert.Contains(t, row["detail"], value)
		}
		assert.Contains(t, row["detail"], "CODEX_THREAD_ID")
		assert.Contains(t, strings.ToLower(row["detail"]), "harness")
		assert.NotContains(t, row["detail"], "synthetic-thread-value-not-for-log")
	}
	assert.Equal(t, 1, found)
}

func TestDoctorConfirmations_TextAndJSON_ListSourceTimeMarkerAndSkippedBoard(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	t.Setenv("CODEX_THREAD_ID", "synthetic-thread-value-not-for-log")
	permissionDraftRegister(t, root, "permission-fixture")
	allowed := doctorCLIExecute(t, root, "", "agent", "allow", "permission-fixture", "--yes")
	require.Equal(t, 0, allowed.exit, allowed.stderr)
	log := permissionDraftLog(t)
	require.Len(t, log, 1)
	jsonOut := doctorCLIExecute(t, root, "", "doctor", "--json")
	assert.Equal(t, 0, jsonOut.exit, jsonOut.stderr)
	permissionDraftAssertConfirmationRow(t, jsonOut.stdout, log[0])
	text := doctorCLIExecute(t, root, "", "doctor")
	assert.Equal(t, 0, text.exit, text.stderr)
	assert.Contains(t, text.stdout, "[INFO] agents.confirmations")
	for _, field := range []string{"time", "source", "mode", "board_line"} {
		assert.Contains(t, text.stdout, log[0][field])
	}
	assert.Contains(t, text.stdout, "CODEX_THREAD_ID")
	assert.NotContains(t, text.stdout+text.stderr, "synthetic-thread-value-not-for-log")
	_, err := os.Stat(filepath.Join(root, ".mtix", "confirmations.jsonl"))
	assert.True(t, os.IsNotExist(err), "confirmation detail must remain operator-local")
}

func TestDoctorAgentsUnconfirmed_FixPrintsManualCommandWithoutCreatingGrant(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	permissionDraftRegister(t, root, "permission-fixture")
	permissionDraftMarkHub(t, root)
	_, dir := permissionDraftState(t)
	grantPath := filepath.Join(dir, "grants.json")
	_, err := os.Stat(grantPath)
	require.True(t, os.IsNotExist(err))
	out := doctorCLIExecute(t, root, "", "doctor", "--json", "--fix")
	assert.Equal(t, 2, out.exit, out.stderr)
	found := 0
	for _, row := range doctorCLIRows(t, out.stdout) {
		if row["id"] != "agents.unconfirmed" {
			continue
		}
		found++
		assert.Contains(t, row["detail"], "permission-fixture")
		assert.Contains(t, row["fix"], "mtix agent allow permission-fixture")
		assert.Contains(t, []string{"warn", "fail"}, row["status"])
	}
	assert.Equal(t, 1, found)
	_, err = os.Stat(grantPath)
	assert.True(t, os.IsNotExist(err), "doctor --fix must not authorize an agent")
	_, err = os.Stat(filepath.Join(dir, "confirmations.jsonl"))
	assert.True(t, os.IsNotExist(err), "doctor --fix must not log a synthetic confirmation")
}

func TestDoctorAgentsUnconfirmed_NeverHubRegisteredID_ImplicitAndNoGrantCreated(t *testing.T) {
	root := doctorCLIBoard(t, "")
	permissionDraftClearMarkers(t)
	permissionDraftRegister(t, root, "permission-fixture")
	out := doctorCLIExecute(t, root, "", "doctor", "--json", "--fix")
	assert.Equal(t, 0, out.exit, out.stderr)
	for _, row := range doctorCLIRows(t, out.stdout) {
		if row["id"] == "agents.unconfirmed" {
			assert.Contains(t, []string{"pass", "info"}, row["status"])
			assert.NotContains(t, row["fix"], "mtix agent allow permission-fixture")
		}
	}
	_, dir := permissionDraftState(t)
	for _, name := range []string{"grants.json", "confirmations.jsonl"} {
		_, err := os.Stat(filepath.Join(dir, name))
		assert.True(t, os.IsNotExist(err), "implicit local eligibility needs no confirmation")
	}
}
