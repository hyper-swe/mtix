// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// interleavingStore is a real store that runs `between` once, just before the
// wake pass writes its first node: after the pass selected the node, before
// it wakes it. That is where a concurrent claim or re-defer can land.
type interleavingStore struct {
	*sqlite.Store
	once    sync.Once
	between func()
}

// TransitionStatus runs the interleaved change, then transitions.
func (w *interleavingStore) TransitionStatus(
	ctx context.Context, id string, to model.Status, reason, author string,
) error {
	w.once.Do(w.between)
	return w.Store.TransitionStatus(ctx, id, to, reason, author)
}

// WakeDeferredNode runs the interleaved change, then wakes.
func (w *interleavingStore) WakeDeferredNode(ctx context.Context, id string, now time.Time) (bool, error) {
	w.once.Do(w.between)
	waker, ok := any(w.Store).(interface {
		WakeDeferredNode(context.Context, string, time.Time) (bool, error)
	})
	if !ok {
		return false, errors.New("store has no WakeDeferredNode")
	}
	return waker.WakeDeferredNode(ctx, id, now)
}

// TestWakeDeferredNodes_ChangedBetweenSelectAndWake_IsSkipped verifies the
// wake pass re-checks each node inside the transaction that wakes it: a claim
// or a re-defer that lands after the pass selected the node is kept, not
// overridden by a reopen (MTIX-95.22 round 3).
func TestWakeDeferredNodes_ChangedBetweenSelectAndWake_IsSkipped(t *testing.T) {
	now := time.Date(2030, 6, 1, 12, 0, 0, 0, time.UTC)
	past := now.Add(-time.Hour)
	future := now.Add(time.Hour)
	tests := []struct {
		name       string
		between    func(t *testing.T, s *sqlite.Store, id string)
		wantStatus model.Status
		wantUntil  *time.Time
	}{
		{"claimed", func(t *testing.T, s *sqlite.Store, id string) {
			require.NoError(t, s.ClaimNode(context.Background(), id, "agent-1"))
		}, model.StatusInProgress, nil},
		{"re-deferred to the future", func(t *testing.T, s *sqlite.Store, id string) {
			require.NoError(t, s.DeferNode(context.Background(), id, &future, "later", "agent-1"))
		}, model.StatusDeferred, &future},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clk := func() time.Time { return now }
			s, err := sqlite.New(t.TempDir(), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = s.Close() })
			s.SetClock(clk)
			ctx := context.Background()
			svc := service.NewNodeService(s, nil, nil, nil, clk)
			node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "Race", Creator: "a"})
			require.NoError(t, err)
			require.NoError(t, svc.DeferNode(ctx, node.ID, &past, "deferred", "agent-a"))
			w := &interleavingStore{Store: s}
			w.between = func() { tt.between(t, s, node.ID) }
			bg := service.NewBackgroundService(w, nil, nil, clk)

			require.NoError(t, bg.RunScan(ctx))

			got, err := s.GetNode(ctx, node.ID)
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, got.Status, "the interleaved change is kept")
			if tt.wantUntil == nil {
				assert.Nil(t, got.DeferUntil)
				return
			}
			require.NotNil(t, got.DeferUntil)
			assert.True(t, got.DeferUntil.Equal(*tt.wantUntil))
		})
	}
}
