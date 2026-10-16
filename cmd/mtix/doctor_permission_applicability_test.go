// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestDoctorPermissionApplicability_NoBoard(t *testing.T) {
	saveAndResetApp(t)
	t.Chdir(t.TempDir())
	testutil.OperatorState(t)
	assertNoApplicablePermissionChecks(t)
}

func TestDoctorPermissionApplicability_HooksOnly(t *testing.T) {
	reviewProject(t)
	assertNoApplicablePermissionChecks(t)
}

func assertNoApplicablePermissionChecks(t *testing.T) {
	t.Helper()
	svc, err := newConfiguredDoctorService()
	require.NoError(t, err)
	rows, err := svc.Run(context.Background(), service.DoctorRunOptions{})
	require.NoError(t, err)
	for _, row := range rows {
		require.False(t, strings.HasPrefix(row.ID, "agents."), row.ID)
	}
}

func TestDoctorPermissionApplicability_OpenedBoardStillFails(t *testing.T) {
	for _, kind := range []string{"unreadable_state", "tampered_grants"} {
		t.Run(kind, func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			state, dir := permissionDraftState(t)
			path := dir
			if kind == "unreadable_state" {
				require.NoError(t, os.MkdirAll(filepath.Dir(dir), 0o700))
				require.NoError(t, os.WriteFile(path, []byte("not an operator-state directory"), 0o600))
			} else {
				_, err := permissionCobraExecute(t, root, "agent", "allow", "future-agent", "--yes")
				require.NoError(t, err)
				require.NoError(t, state.WriteJSON("grants", map[string]any{"agent_grants": "tampered"}))
				path = filepath.Join(dir, "grants.json")
			}
			before, err := os.ReadFile(path)
			require.NoError(t, err)
			for _, fix := range []bool{false, true} {
				args := []string{"doctor", "--json"}
				if fix {
					args = append(args, "--fix")
				}
				out := doctorCLIExecute(t, root, "", args...)
				require.Equal(t, 2, out.exit, out.stderr)
				row := permissionCobraDoctorRow(t, out.stdout, "agents.confirmations")
				require.Equal(t, string(service.DoctorFail), row["status"])
				after, err := os.ReadFile(path)
				require.NoError(t, err)
				require.Equal(t, before, after, "diagnostics preserve refused evidence")
			}
		})
	}
}
