// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

func TestObserveHub_CachedMissingIdentityCannotRecountCanonicalOccurrence(t *testing.T) {
	f := permissionHeldHubFixture(t)
	f.board.ID = ""
	cached := f.board
	permissionBindingFixtureMint(t, f)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	requireHubAlertCount(t, f, 1)
	markBefore, err := f.store.HubMark(t.Context())
	require.NoError(t, err)
	alias := filepath.Join(t.TempDir(), "cached-checkout-alias")
	require.NoError(t, os.Symlink(cached.Checkout, alias))
	for _, checkout := range []string{cached.Checkout, alias} {
		cached.Checkout = checkout
		err := f.svc.ObserveHub(t.Context(), cached)
		if err != nil {
			require.ErrorIs(t, err, model.ErrOperatorStateUnreadable)
		}
		requireHubAlertCount(t, f, 1)
	}
	markAfter, err := f.store.HubMark(t.Context())
	require.NoError(t, err)
	require.Equal(t, markBefore, markAfter)
	dir, err := f.state.Path()
	require.NoError(t, err)
	scope := permissionBindingFixtureLegacyScope(t, f)
	sum := sha256.Sum256([]byte(scope))
	_, err = os.Stat(filepath.Join(dir, "hub-wake-"+hex.EncodeToString(sum[:16])+".json"))
	require.ErrorIs(t, err, os.ErrNotExist, "cached identity must not complete a fresh legacy occurrence")
}
