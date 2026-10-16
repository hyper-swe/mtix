// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func permissionBindingFixtureKey(scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return "wake-hub-binding-" + hex.EncodeToString(sum[:16])
}

func permissionBindingFixtureMint(t *testing.T, f *wakePermissionFixture) {
	t.Helper()
	mtix := filepath.Join(f.board.Checkout, ".mtix")
	require.NoError(t, os.Mkdir(mtix, 0700))
	id, err := service.CreateBoardID(mtix)
	require.NoError(t, err)
	f.board.ID = id
}

func permissionBindingFixtureLegacyScope(t *testing.T, f *wakePermissionFixture) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(f.board.Checkout)
	require.NoError(t, err)
	return "legacy-store:" + root
}

func permissionBindingFixtureReopen(t *testing.T, f *wakePermissionFixture) {
	t.Helper()
	require.NoError(t, f.store.Close())
	st, err := sqlite.New(filepath.Join(f.board.Checkout, "board.db"), nil)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	f.store = st
	deps := f.svc.PermissionDeps
	deps.Store = st
	f.svc = service.NewWakePermissions(deps)
}

func TestObserveHub_BindingAdoptionReopenAndRealNextEpoch(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.board.ID = ""
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	permissionBindingFixtureMint(t, f)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	permissionBindingFixtureReopen(t, f)
	alias := f.board
	alias.Checkout = filepath.Join(t.TempDir(), "another-checkout-of-same-board")
	require.NoError(t, f.svc.ObserveHub(t.Context(), alias))
	requireHubAlertCount(t, f, 1)
	_, err := f.store.ResetHubMark(t.Context())
	require.NoError(t, err)
	_, err = f.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), alias))
	requireHubAlertCount(t, f, 2)
}

func TestObserveHub_BoundPhysicalCheckoutRejectsDifferentBoardID(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.board.ID = ""
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	permissionBindingFixtureMint(t, f)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	path := filepath.Join(f.board.Checkout, ".mtix", "board-id")
	other := "22222222-2222-4222-8222-222222222222"
	require.NoError(t, os.WriteFile(path, []byte(other+"\n"), 0644))
	f.board.ID = other
	require.Error(t, f.svc.ObserveHub(t.Context(), f.board))
	requireHubAlertCount(t, f, 1)
}

func TestObserveHub_MalformedBindingFailsWithoutAlertOrRepair(t *testing.T) {
	f := permissionHeldHubFixture(t)
	key := permissionBindingFixtureKey("board:" + f.board.ID)
	require.NoError(t, f.state.WriteJSON(key, map[string]any{"identity": 123}))
	dir, err := f.state.Path()
	require.NoError(t, err)
	path := filepath.Join(dir, key+".json")
	before, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Error(t, f.svc.ObserveHub(t.Context(), f.board))
	after, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, before, after)
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestObserveHub_OldPartialReceiptWithoutBindingAdoptsValidatedIdentity(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.register(t, "second")
	f.board.ID = ""
	cause := errors.New("synthetic second registration lookup failure")
	f.svc.Store = &permissionPartialRegistrationFailure{PermissionStore: f.store, cause: cause}
	require.ErrorIs(t, f.svc.ObserveHub(t.Context(), f.board), cause)
	requirePermissionAgentAlertCounts(t, f, map[string]uint64{"first": 1})
	dir, err := f.state.Path()
	require.NoError(t, err)
	// Qualified old-candidate state: remove only the new binding, retaining real receipts.
	key := permissionBindingFixtureKey(permissionBindingFixtureLegacyScope(t, f))
	require.NoError(t, os.Remove(filepath.Join(dir, key+".json")))
	f.svc.Store = f.store
	permissionBindingFixtureMint(t, f)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requirePermissionAgentAlertCounts(t, f, map[string]uint64{"first": 1, "second": 1})
}

func TestObserveHub_BoundIntentWithoutCanonicalAliasRecoversAfterReopen(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.board.ID = ""
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	physical := permissionBindingFixtureLegacyScope(t, f)
	permissionBindingFixtureMint(t, f)
	// Qualified crash-boundary fixture: first binding persisted, alias not yet written.
	require.NoError(t, f.state.WriteJSON(permissionBindingFixtureKey(physical), map[string]any{
		"identity": physical, "board_id": f.board.ID,
	}))
	permissionBindingFixtureReopen(t, f)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requireHubAlertCount(t, f, 1)
}

// Inject an actual final observation replacement failure after the first real Alert.
type permissionObservationWriteBlock struct {
	service.PermissionStore
	calls int
	path  string
}

func (s *permissionObservationWriteBlock) ListRegistrations(ctx context.Context, host, uid string) ([]sqlite.Registration, error) {
	s.calls++
	if s.calls == 3 {
		if err := os.Mkdir(s.path, 0700); err != nil {
			return nil, err
		}
	}
	return s.PermissionStore.ListRegistrations(ctx, host, uid)
}

func TestObserveHub_RealObservationWriteFailureThenMintDoesNotRecount(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.register(t, "second")
	f.board.ID = ""
	dir, err := f.state.Path()
	require.NoError(t, err)
	scope := permissionBindingFixtureLegacyScope(t, f)
	sum := sha256.Sum256([]byte(scope))
	blocked := filepath.Join(dir, "hub-wake-"+hex.EncodeToString(sum[:16])+".json")
	fault := &permissionObservationWriteBlock{PermissionStore: f.store, path: blocked}
	f.svc.Store = fault
	require.Error(t, f.svc.ObserveHub(t.Context(), f.board))
	require.Equal(t, 3, fault.calls)
	requirePermissionAgentAlertCounts(t, f, map[string]uint64{"first": 1, "second": 1})
	require.NoError(t, os.Remove(blocked))
	f.svc.Store = f.store
	permissionBindingFixtureMint(t, f)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requirePermissionAgentAlertCounts(t, f, map[string]uint64{"first": 1, "second": 1})
}
