// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReviewR1_DoctorOrphanPermissionStateKeepsReportWithoutMintingHost(t *testing.T) {
	for _, badType := range []bool{false, true} {
		t.Run(map[bool]string{false: "present-record", true: "unreadable-record"}[badType], func(t *testing.T) {
			root := doctorCLIBoard(t, filepath.Join(t.TempDir(), "unavailable-relay"))
			state, dir := permissionDraftState(t)
			require.NoError(t, state.Ensure())
			if badType {
				require.NoError(t, os.Mkdir(filepath.Join(dir, "permissions-pending.json"), 0700))
			} else {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "grants.json"), []byte("{}"), 0600))
			}
			output := doctorCLIExecute(t, root, "", "doctor", "--json")
			reviewR1DoctorFailureRows(t, output)
			_, err := os.Stat(filepath.Join(dir, "host-id"))
			require.ErrorIs(t, err, os.ErrNotExist)
		})
	}
}

func TestReviewR1_DoctorInvalidEnvironmentStillProducesPermissionFailureRows(t *testing.T) {
	root := doctorCLIBoard(t, filepath.Join(t.TempDir(), "unavailable-relay"))
	t.Setenv("XDG_CONFIG_HOME", "relative-config")
	t.Setenv("APPDATA", "relative-config")
	output := doctorCLIExecute(t, root, "", "doctor", "--json")
	reviewR1DoctorFailureRows(t, output)
}

func TestReviewR1_DoctorUnreadableBoardObservationRetainsOtherChecks(t *testing.T) {
	root := doctorCLIBoard(t, filepath.Join(t.TempDir(), "unavailable-relay"))
	canonical, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)
	state, dir := permissionDraftState(t)
	require.NoError(t, state.ObserveBoard(canonical, "11111111-1111-4111-8111-111111111111", oplocal.HubIdentity{}))
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	found := false
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "identity-") {
			found = true
			require.NoError(t, os.WriteFile(filepath.Join(dir, entry.Name()), []byte("{synthetic observation corruption"), 0600))
		}
	}
	require.True(t, found)
	_, _, fixtureErr := state.BoardObservation(canonical)
	require.Error(t, fixtureErr)
	output := doctorCLIExecute(t, root, "", "doctor", "--json")
	reviewR1DoctorFailureRows(t, output)
}

func TestReviewR1_RecoveryJSONWriteFailureIsReturned(t *testing.T) {
	saveAndResetApp(t)
	state := identityOperatorState(t)
	_, err := state.HostID()
	require.NoError(t, err)
	app.jsonOutput = true
	cause := errors.New("synthetic closed output")
	command := newAgentPermissionRecoveryCmd()
	command.SetOut(&doctorNthWriter{remaining: 1, cause: cause})
	command.SetErr(&bytes.Buffer{})
	require.ErrorIs(t, command.ExecuteContext(t.Context()), cause)
}

func TestReviewR1_RecoveryInvalidEnvironmentAndUnreadableStateAreReported(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
		saveAndResetApp(t)
		t.Setenv("XDG_CONFIG_HOME", "relative-config")
		t.Setenv("APPDATA", "relative-config")
		command := newAgentPermissionRecoveryCmd()
		command.SetOut(&bytes.Buffer{})
		command.SetErr(&bytes.Buffer{})
		require.Error(t, command.ExecuteContext(t.Context()))
	})
	t.Run("state-file", func(t *testing.T) {
		root := doctorCLIBoard(t, "")
		_, dir := permissionDraftState(t)
		require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0700))
		require.NoError(t, os.WriteFile(dir, []byte("synthetic unavailable state"), 0600))
		output := doctorCLIExecute(t, root, "", "agent", "permissions", "recover")
		assert.NotEqual(t, 0, output.exit)
		data, err := os.ReadFile(dir)
		require.NoError(t, err)
		assert.Equal(t, "synthetic unavailable state", string(data))
	})
}
