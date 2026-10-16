// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

type permissionPartialRegistrationFailure struct {
	service.PermissionStore
	calls int
	cause error
}

func (s *permissionPartialRegistrationFailure) ListRegistrations(ctx context.Context, host, uid string) ([]sqlite.Registration, error) {
	s.calls++
	if s.calls == 3 {
		return nil, s.cause
	}
	return s.PermissionStore.ListRegistrations(ctx, host, uid)
}

func requirePermissionAgentAlertCounts(t *testing.T, f *wakePermissionFixture, expected map[string]uint64) {
	t.Helper()
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, len(expected))
	actual := make(map[string]uint64, len(rows))
	for _, row := range rows {
		require.Equal(t, "wake.unconfirmed", row.Class)
		require.Equal(t, "mtix agent allow "+row.Agent, row.Fix)
		actual[row.Agent] = row.Count
	}
	require.Equal(t, expected, actual)
}

func TestObserveHub_PartialLegacyAlertThenMintKeepsEachRealOccurrenceOnce(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.register(t, "second")
	f.board.ID = ""
	cause := errors.New("synthetic second registration lookup failure")
	fault := &permissionPartialRegistrationFailure{PermissionStore: f.store, cause: cause}
	f.svc.Store = fault
	require.ErrorIs(t, f.svc.ObserveHub(t.Context(), f.board), cause)
	require.Equal(t, 3, fault.calls)
	requirePermissionAgentAlertCounts(t, f, map[string]uint64{"first": 1})
	dir, err := f.state.Path()
	require.NoError(t, err)
	observations, err := filepath.Glob(filepath.Join(dir, "hub-wake-*.json"))
	require.NoError(t, err)
	require.Empty(t, observations, "no completed observation follows the partial failure")
	f.svc.Store = f.store
	markBefore, err := f.store.HubMark(t.Context())
	require.NoError(t, err)
	mtix := filepath.Join(f.board.Checkout, ".mtix")
	require.NoError(t, os.Mkdir(mtix, 0700))
	f.board.ID, err = service.CreateBoardID(mtix)
	require.NoError(t, err)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	markAfter, err := f.store.HubMark(t.Context())
	require.NoError(t, err)
	require.Equal(t, markBefore, markAfter)
	requirePermissionAgentAlertCounts(t, f, map[string]uint64{"first": 1, "second": 1})
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requirePermissionAgentAlertCounts(t, f, map[string]uint64{"first": 1, "second": 1})
}
