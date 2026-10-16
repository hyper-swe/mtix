// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Invalid eligibility and Alert input must be rejected before any operator write.
package service_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func permissionBoundaryStateBytes(t *testing.T, f *wakePermissionFixture) map[string]string {
	t.Helper()
	dir, err := f.state.Path()
	require.NoError(t, err)
	entries, err := os.ReadDir(dir)
	require.NoError(t, err)
	result := map[string]string{}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		require.NoError(t, err)
		result[entry.Name()] = string(data)
	}
	return result
}

func TestWakeable_InvalidAgentIdentityCannotBecomeImplicitEligibility(t *testing.T) {
	for _, agent := range []string{strings.Repeat("x", 65), "invalid\nagent"} {
		t.Run(agent, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			before := permissionBoundaryStateBytes(t, f)
			ok, reason, err := f.svc.Wakeable(t.Context(), f.board, agent)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			require.False(t, ok)
			require.Equal(t, "not_registered", reason)
			require.Equal(t, before, permissionBoundaryStateBytes(t, f))
		})
	}
}

func TestRaisePermissionTransition_InvalidInputDoesNotCreateProtectedRecords(t *testing.T) {
	for _, tc := range []struct{ name, agent, detail string }{
		{"agent", strings.Repeat("x", 65), "synthetic boundary input"},
		{"detail", "first", strings.Repeat("x", 4097)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWakePermissionFixture(t)
			before := permissionBoundaryStateBytes(t, f)
			alerts := service.NewAlerts(f.state, f.svc.Clock)
			row, err := alerts.RaisePermissionTransition(t.Context(), service.AlertRequest{
				Agent: tc.agent, Class: "wake.unconfirmed", Detail: tc.detail, Fix: "mtix agent allow first",
			}, oplocal.PermissionAlertOccurrence{Slot: "synthetic-board-agent", Key: "synthetic-transition"})
			require.Error(t, err)
			require.Equal(t, oplocal.Alert{}, row)
			require.Equal(t, before, permissionBoundaryStateBytes(t, f))
		})
	}
}
