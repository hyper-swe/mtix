// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/require"
)

func permissionHeldHubFixture(t *testing.T) *wakePermissionFixture {
	t.Helper()
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	flipped, err := f.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	require.True(t, flipped)
	return f
}

func requireHubAlertCount(t *testing.T, f *wakePermissionFixture, count int64) {
	t.Helper()
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.EqualValues(t, count, rows[0].Count)
	require.Equal(t, "first", rows[0].Agent)
	require.Equal(t, "wake.unconfirmed", rows[0].Class)
	require.Equal(t, "mtix agent allow first", rows[0].Fix)
}

func TestObserveHub_LegacyAliasAndMintedBoardIdentityKeepOneRealOccurrence(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.board.ID = ""
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	alias := filepath.Join(t.TempDir(), "checkout-alias")
	require.NoError(t, os.Symlink(f.board.Checkout, alias))
	aliased := f.board
	aliased.Checkout = alias
	require.NoError(t, f.svc.ObserveHub(t.Context(), aliased))
	requireHubAlertCount(t, f, 1)
	mtix := filepath.Join(f.board.Checkout, ".mtix")
	require.NoError(t, os.Mkdir(mtix, 0700))
	id, err := service.CreateBoardID(mtix)
	require.NoError(t, err)
	f.board.ID = id
	markBefore, err := f.store.HubMark(t.Context())
	require.NoError(t, err)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	markAfter, err := f.store.HubMark(t.Context())
	require.NoError(t, err)
	require.Equal(t, markBefore, markAfter, "identity minting did not create a new hub epoch")
	requireHubAlertCount(t, f, 1)
}

func TestObserveHub_LegacyUnresolvedCheckoutRepeatsOneOccurrence(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.board.ID = ""
	f.board.Checkout = filepath.Join(f.board.Checkout, "missing-checkout")
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requireHubAlertCount(t, f, 1)
}

func TestObserveHub_RegistrationFailureDoesNotStampObservedSuccess(t *testing.T) {
	f := permissionHeldHubFixture(t)
	cause := errors.New("synthetic transition registration failure")
	f.svc.Store = permissionReadFailure{PermissionStore: f.store, registration: cause}
	require.ErrorIs(t, f.svc.ObserveHub(t.Context(), f.board), cause)
	dir, err := f.state.Path()
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(dir, "hub-wake-*.json"))
	require.NoError(t, err)
	require.Empty(t, files)
	f.svc.Store = f.store
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requireHubAlertCount(t, f, 1)
}

func TestObserveHub_MalformedObservationNeverRecountsOrRewrites(t *testing.T) {
	f := permissionHeldHubFixture(t)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	dir, err := f.state.Path()
	require.NoError(t, err)
	files, err := filepath.Glob(filepath.Join(dir, "hub-wake-*.json"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	key := strings.TrimSuffix(filepath.Base(files[0]), ".json")
	require.NoError(t, f.state.WriteJSON(key, map[string]any{"epoch": 123}))
	before, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.ErrorIs(t, f.svc.ObserveHub(t.Context(), f.board), model.ErrOperatorStateUnreadable)
	after, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Equal(t, before, after)
	requireHubAlertCount(t, f, 1)
}

func TestObserveHub_ProtectedAlertFailureRetriesExactlyOnce(t *testing.T) {
	f := permissionHeldHubFixture(t)
	dir, err := f.state.Path()
	require.NoError(t, err)
	blocked := filepath.Join(dir, "alerts.jsonl")
	require.NoError(t, os.Mkdir(blocked, 0700))
	require.Error(t, f.svc.ObserveHub(t.Context(), f.board))
	files, err := filepath.Glob(filepath.Join(dir, "hub-wake-*.json"))
	require.NoError(t, err)
	require.Empty(t, files)
	require.NoError(t, os.Remove(blocked))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requireHubAlertCount(t, f, 1)
}
