// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Doctor reports enrollment without granting it or inventing future setup commands.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestHostDispatcher_UnenrolledWorktree_DoctorReportsWithoutGrant(t *testing.T) {
	initRegistrationApp(t)
	root := filepath.Dir(app.mtixDir)
	common := filepath.Join(t.TempDir(), ".git")
	admin := filepath.Join(common, "worktrees", "lane")
	require.NoError(t, os.MkdirAll(admin, 0700))
	require.NoError(t, os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: "+admin+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "commondir"), []byte(common+"\n"), 0600))
	require.NoError(t, os.WriteFile(filepath.Join(admin, "gitdir"), []byte(filepath.Join(root, ".git")+"\n"), 0600))
	require.NoError(t, runAgentRegister("test-worker", sessionOptions{}))
	_, err := app.store.MarkHubConnected(t.Context(), "applied_events")
	require.NoError(t, err)
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerHostDispatcherDoctor(svc))
	rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
	require.NoError(t, err)
	found := map[string]service.DoctorResult{}
	for _, row := range rows {
		found[row.ID] = row
		require.NotContains(t, row.Fix, "mtix setup")
		require.NotContains(t, row.Fix, "dispatcher enroll")
	}
	require.Contains(t, found, "dispatcher.host")
	require.Contains(t, found, "board.enrolled")
	require.Contains(t, found, "board.worktree-unenrolled")
	state, err := operatorState()
	require.NoError(t, err)
	registry, err := state.DispatchBoards(t.Context())
	require.NoError(t, err)
	require.Empty(t, registry.Enrolled)
	require.Empty(t, registry.History)
}

func TestHostDispatcher_UnreadableRegistry_DefaultOffWarnsInstalledFails(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed bool
		want      service.DoctorStatus
	}{{"default-off", false, service.DoctorWarn}, {"installed", true, service.DoctorFail}} {
		t.Run(tc.name, func(t *testing.T) {
			initRegistrationApp(t)
			require.NoError(t, runAgentRegister("registry-worker", sessionOptions{}))
			state, err := operatorState()
			require.NoError(t, err)
			require.NoError(t, state.WriteJSON("dispatcher-service", hostServiceRecord{Installed: tc.installed}))
			require.NoError(t, state.WriteJSON("boards", []string{"invalid registry"}))
			svc := service.NewDoctorService(service.DoctorOptions{})
			require.NoError(t, registerHostDispatcherDoctor(svc))
			rows, runErr := svc.Run(t.Context(), service.DoctorRunOptions{})
			if tc.installed {
				require.ErrorIs(t, runErr, service.ErrDoctorChecksFailed)
			} else {
				require.NoError(t, runErr)
			}
			require.Len(t, rows, 1)
			require.Equal(t, "dispatcher.host", rows[0].ID)
			require.Equal(t, tc.want, rows[0].Status)
			require.Contains(t, rows[0].Detail, "cannot be read")
		})
	}
}

func TestHostDispatcher_EnrollmentDoctorError_ReportsRow(t *testing.T) {
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "default-off", true: "installed"}[installed], func(t *testing.T) {
			initRegistrationApp(t)
			require.NoError(t, runAgentRegister("doctor-worker", sessionOptions{}))
			state, err := operatorState()
			require.NoError(t, err)
			require.NoError(t, state.WriteJSON("dispatcher-service", hostServiceRecord{Installed: installed}))
			require.NoError(t, app.store.Close())
			svc := service.NewDoctorService(service.DoctorOptions{})
			require.NoError(t, registerHostDispatcherDoctor(svc))
			rows, runErr := svc.Run(t.Context(), service.DoctorRunOptions{})
			if installed {
				require.ErrorIs(t, runErr, service.ErrDoctorChecksFailed)
			} else {
				require.NoError(t, runErr)
			}
			found := false
			for _, row := range rows {
				if row.ID == "board.dispatcher-metadata" {
					found = true
					require.Contains(t, row.Detail, "cannot inspect")
					if installed {
						require.Equal(t, service.DoctorFail, row.Status)
					} else {
						require.Equal(t, service.DoctorWarn, row.Status)
					}
				}
			}
			require.True(t, found)
		})
	}
}

func TestHostDispatcher_ConflictingBoardID_DoctorRoutingWarns(t *testing.T) {
	initRegistrationApp(t)
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "board-id"), []byte(uuid.NewString()+"\n"+uuid.NewString()+"\n"), 0600))
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerHostDispatcherDoctor(svc))
	rows, err := svc.Run(t.Context(), service.DoctorRunOptions{})
	require.NoError(t, err)
	found := false
	for _, row := range rows {
		if row.ID == "board.routing" {
			found = true
			require.Equal(t, service.DoctorWarn, row.Status)
			require.Contains(t, row.Detail, "board identity conflict")
		}
	}
	require.True(t, found)
}
