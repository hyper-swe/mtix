// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Protected binding corruption must never create an Alert or repair identities.
package service_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestObserveHub_InvalidBindingIdentityRejectsWithoutPublication(t *testing.T) {
	for name, binding := range map[string]map[string]string{
		"missing_identity":       {},
		"unknown_scope":          {"identity": "other:fixture"},
		"invalid_uuid":           {"identity": "board:bad", "board_id": "bad"},
		"nil_uuid":               {"identity": "board:00000000-0000-0000-0000-000000000000", "board_id": "00000000-0000-0000-0000-000000000000"},
		"inconsistent_canonical": {"identity": "board:22222222-2222-4222-8222-222222222222", "board_id": "11111111-1111-4111-8111-111111111111"},
		"missing_canonical_uuid": {"identity": "legacy-store:fixture"},
	} {
		t.Run(name, func(t *testing.T) {
			f := permissionHeldHubFixture(t)
			key := permissionBindingFixtureKey("board:" + f.board.ID)
			require.NoError(t, f.state.WriteJSON(key, binding))
			requireBindingRefusalUnchanged(t, f, key)
		})
	}
}

func requireBindingRefusalUnchanged(t *testing.T, f *wakePermissionFixture, key string) {
	t.Helper()
	dir, err := f.state.Path()
	require.NoError(t, err)
	path := filepath.Join(dir, key+".json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	mark, err := f.store.HubMark(t.Context())
	require.NoError(t, err)
	require.ErrorIs(t, f.svc.ObserveHub(t.Context(), f.board), model.ErrOperatorStateUnreadable)
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	current, err := f.store.HubMark(t.Context())
	require.NoError(t, err)
	require.Equal(t, mark, current)
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestObserveHub_LegacyPhysicalBindingCannotNameAnotherCheckout(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.board.ID = ""
	key := permissionBindingFixtureKey(permissionBindingFixtureLegacyScope(t, f))
	require.NoError(t, f.state.WriteJSON(key, map[string]string{"identity": "legacy-store:another-checkout"}))
	requireBindingRefusalUnchanged(t, f, key)
}

func TestObserveHub_StaleLegacyCallerCannotReopenBoundPhysicalScope(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.board.ID = ""
	key := permissionBindingFixtureKey(permissionBindingFixtureLegacyScope(t, f))
	require.NoError(t, f.state.WriteJSON(key, map[string]string{
		"identity": permissionBindingFixtureLegacyScope(t, f), "board_id": "11111111-1111-4111-8111-111111111111",
	}))
	requireBindingRefusalUnchanged(t, f, key)
}

func TestObserveHub_UnavailableBindingCanRetryWithoutFalseCompletion(t *testing.T) {
	f := permissionHeldHubFixture(t)
	key := permissionBindingFixtureKey("board:" + f.board.ID)
	dir, err := f.state.Path()
	require.NoError(t, err)
	blocked := filepath.Join(dir, key+".json")
	require.NoError(t, os.Mkdir(blocked, 0700))
	require.Error(t, f.svc.ObserveHub(t.Context(), f.board))
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, rows)
	require.NoError(t, os.Remove(blocked))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requireHubAlertCount(t, f, 1)
}
