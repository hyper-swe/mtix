// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func reviewR1OperatorFiles(t *testing.T, dir string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	files := map[string]string{}
	for _, entry := range entries {
		data, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, readErr)
		files[entry.Name()] = string(data)
	}
	return files
}

func TestReviewR1_DoctorPreservesBoardMarkPersistenceWithoutCreatingOperatorFiles(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "absent-state", true: "existing-state"}[existing], func(t *testing.T) {
			root := doctorCLIBoard(t, filepath.Join(t.TempDir(), "unavailable-relay"))
			state, dir := permissionDraftState(t)
			before := map[string]string{}
			if existing {
				require.NoError(t, state.WriteJSON("sentinel", map[string]string{"value": "retained"}))
				before = reviewR1OperatorFiles(t, dir)
			}
			st, err := sqlite.New(filepath.Join(root, ".mtix", "data", "mtix.db"), nil)
			require.NoError(t, err)
			defer func() { require.NoError(t, st.Close()) }()
			mark, err := st.HubMark(t.Context())
			require.NoError(t, err)
			require.False(t, mark.Set)
			output := doctorCLIExecute(t, root, "", "doctor", "--json")
			require.NotEmpty(t, doctorCLIRows(t, output.stdout))
			mark, err = st.HubMark(t.Context())
			require.NoError(t, err)
			assert.True(t, mark.Set, "preserve the board-mark persistence verified on origin/main")
			if existing {
				assert.Equal(t, before, reviewR1OperatorFiles(t, dir))
			} else {
				_, err = os.Stat(dir)
				require.ErrorIs(t, err, os.ErrNotExist)
			}
		})
	}
}

func TestReviewR1_TopLevelDoctorReportsDeadRegistrationWithoutExpiringIt(t *testing.T) {
	testutil.OperatorState(t)
	initRegistrationApp(t)
	captureStdout(t, func() { require.NoError(t, runAgentRegister("stale-lane", sessionOptions{Session: "synthetic"})) })
	_, err := app.store.WriteDB().Exec(`UPDATE agent_registrations SET pid = ?, pid_start_time = ?, last_seen = ? WHERE agent_id = ?`, int64(os.Getpid()), "synthetic-different-start", "2000-01-01T00:00:00Z", "stale-lane")
	require.NoError(t, err)
	_, err = app.configSvc.Set("sync.relay.dir", filepath.Join(t.TempDir(), "unavailable-relay"))
	require.NoError(t, err)
	state, dir := permissionDraftState(t)
	host, err := state.ReadHostID()
	require.NoError(t, err)
	uid, err := currentUserID()
	require.NoError(t, err)
	before, err := app.store.ListRegistrations(t.Context(), host, uid)
	require.NoError(t, err)
	require.Len(t, before, 1)
	files := reviewR1OperatorFiles(t, dir)
	output := doctorCLIExecute(t, filepath.Dir(app.mtixDir), "", "doctor", "--json")
	found := false
	for _, row := range doctorCLIRows(t, output.stdout) {
		if row["id"] == "sync.agents.stale" {
			found = true
			assert.Contains(t, row["detail"], "stale-lane")
			assert.Contains(t, row["fix"], "mtix agent unregister stale-lane --force")
		}
	}
	require.True(t, found)
	after, err := app.store.ListRegistrations(t.Context(), host, uid)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, files, reviewR1OperatorFiles(t, dir))
}

func TestReviewR1_DoctorStartupScopeIsTopLevelOnly(t *testing.T) {
	root := newRootCmd()
	doctor, _, err := root.Find([]string{"doctor"})
	require.NoError(t, err)
	legacy, _, err := root.Find([]string{"sync", "doctor"})
	require.NoError(t, err)
	show, _, err := root.Find([]string{"show"})
	require.NoError(t, err)
	assert.True(t, isTopLevelDoctor(doctor))
	assert.False(t, isTopLevelDoctor(legacy))
	assert.False(t, isTopLevelDoctor(show))
}
