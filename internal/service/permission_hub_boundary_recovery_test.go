// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Unreadable association evidence must never be replaced by a fresh hub receipt.
package service_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func TestObserveHub_DamagedPhysicalOrCompletedAssociationIsNotReinitialized(t *testing.T) {
	for _, kind := range []string{"physical_binding", "legacy_observation", "canonical_observation", "canonical_other_uuid"} {
		t.Run(kind, func(t *testing.T) {
			f := permissionHeldHubFixture(t)
			physical := permissionBindingFixtureLegacyScope(t, f)
			key := permissionBindingFixtureKey(physical)
			var data any = map[string]int{"identity": 123}
			if kind == "legacy_observation" {
				f.board.ID = ""
				key = strings.Replace(key, "wake-hub-binding-", "hub-wake-", 1)
				data = map[string]int{"epoch": 123}
			}
			if kind == "canonical_observation" {
				key = strings.Replace(permissionBindingFixtureKey("board:"+f.board.ID), "wake-hub-binding-", "hub-wake-", 1)
				data = map[string]int{"epoch": 123}
			}
			if kind == "canonical_other_uuid" {
				key = permissionBindingFixtureKey("board:" + f.board.ID)
				data = map[string]string{"identity": "board:22222222-2222-4222-8222-222222222222", "board_id": "22222222-2222-4222-8222-222222222222"}
			}
			require.NoError(t, f.state.WriteJSON(key, data))
			requireBindingRefusalUnchanged(t, f, key)
		})
	}
}

type permissionBindingWriteFault struct {
	service.PermissionStore
	path  string
	fired bool
}

func (s *permissionBindingWriteFault) ListRegistrations(ctx context.Context, host, uid string) ([]sqlite.Registration, error) {
	if !s.fired {
		s.fired = true
		if err := os.Mkdir(s.path, 0700); err != nil {
			return nil, err
		}
	}
	return s.PermissionStore.ListRegistrations(ctx, host, uid)
}

func TestObserveHub_ActualCanonicalBindingWriteFailureDoesNotCompleteEpoch(t *testing.T) {
	f := permissionHeldHubFixture(t)
	dir, err := f.state.Path()
	require.NoError(t, err)
	key := permissionBindingFixtureKey("board:" + f.board.ID)
	blocked := filepath.Join(dir, key+".json")
	fault := &permissionBindingWriteFault{PermissionStore: f.store, path: blocked}
	f.svc.Store = fault
	require.Error(t, f.svc.ObserveHub(t.Context(), f.board))
	require.True(t, fault.fired)
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, rows)
	observation := strings.Replace(key, "wake-hub-binding-", "hub-wake-", 1)
	_, err = os.Stat(filepath.Join(dir, observation+".json"))
	require.ErrorIs(t, err, os.ErrNotExist)
	require.NoError(t, os.Remove(blocked))
	f.svc.Store = f.store
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requireHubAlertCount(t, f, 1)
}
