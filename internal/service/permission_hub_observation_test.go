// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestObserveHub_CurrentEpoch_CachedCallerCannotReplayOlderOccurrence(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	cached := f.board
	for i := int64(1); i <= 4; i++ {
		if i > 1 {
			_, err := f.store.ResetHubMark(t.Context())
			require.NoError(t, err)
		}
		flipped, err := f.store.MarkHubConnected(t.Context(), "config")
		require.NoError(t, err)
		require.True(t, flipped)
		require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
		require.NoError(t, f.svc.ObserveHub(t.Context(), cached))
		rows, err := service.NewAlerts(f.state, func() time.Time { return f.now }).Recent(t.Context(), 0)
		require.NoError(t, err)
		require.Len(t, rows, 1)
		assert.EqualValues(t, i, rows[0].Count, "each real flip counts once; stale board inputs never replay a completed epoch")
		assert.Equal(t, "first", rows[0].Agent)
		assert.Equal(t, "wake.unconfirmed", rows[0].Class)
		assert.Equal(t, "mtix agent allow first", rows[0].Fix)
	}
	otherCheckout := f.board
	otherCheckout.Checkout = filepath.Join(t.TempDir(), "same-board-checkout")
	require.NoError(t, f.svc.ObserveHub(t.Context(), otherCheckout))
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 4, rows[0].Count, "shared board identity does not count once per checkout")
}

func TestObserveHub_ConcurrentReaders_OneAlertAndNoMutationOnLocalBoard(t *testing.T) {
	f := newWakePermissionFixture(t)
	f.register(t, "first")
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
	_, err = f.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	results := make([]error, 16)
	var joined sync.WaitGroup
	for i := range results {
		joined.Add(1)
		go func(index int) { defer joined.Done(); results[index] = f.svc.ObserveHub(ctx, f.board) }(i)
	}
	joined.Wait()
	require.NoError(t, ctx.Err())
	for _, err := range results {
		require.NoError(t, err)
	}
	rows, err = f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	assert.EqualValues(t, 1, rows[0].Count)
}

func TestObserveHub_ConfirmedAndDeniedIDs_NoUnconfirmedAlert(t *testing.T) {
	f := newWakePermissionFixture(t)
	for _, id := range []string{"allowed", "denied"} {
		f.register(t, id)
	}
	f.apply(t, oplocal.PermissionChange{AgentID: "allowed", Action: "allow"}, oplocal.PermissionChange{AgentID: "denied", Action: "deny"})
	_, err := f.store.MarkHubConnected(t.Context(), "config")
	require.NoError(t, err)
	require.NoError(t, f.svc.ObserveHub(t.Context(), f.board))
	rows, err := f.state.RecentAlerts(t.Context(), 0)
	require.NoError(t, err)
	assert.Empty(t, rows)
}
