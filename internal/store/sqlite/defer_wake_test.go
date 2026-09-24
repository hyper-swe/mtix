// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestWakeDeferredNode_RechecksBeforeWaking verifies the wake pass reopens a
// node only if, inside the waking transaction, it is still deferred with a
// wake time at or before now; otherwise it skips it, so a claim or re-defer
// that landed after the pass selected the node is not overridden. A woken
// node loses its wake time and emits the usual transition (MTIX-95.22 round 3).
func TestWakeDeferredNode_RechecksBeforeWaking(t *testing.T) {
	past := deferTestNow.Add(-time.Hour)
	future := deferTestNow.Add(time.Hour)
	tests := []struct {
		name       string
		setup      func(t *testing.T, s *sqlite.Store)
		wantWoken  bool
		wantStatus model.Status
	}{
		{"wake time passed", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.DeferNode(context.Background(), "PROJ-1", &past, "d", "a"))
		}, true, model.StatusOpen},
		{"wake time equal to now", func(t *testing.T, s *sqlite.Store) {
			now := deferTestNow
			require.NoError(t, s.DeferNode(context.Background(), "PROJ-1", &now, "d", "a"))
		}, true, model.StatusOpen},
		{"re-deferred to the future", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.DeferNode(context.Background(), "PROJ-1", &past, "d", "a"))
			require.NoError(t, s.DeferNode(context.Background(), "PROJ-1", &future, "d", "a"))
		}, false, model.StatusDeferred},
		{"claimed since", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.DeferNode(context.Background(), "PROJ-1", &past, "d", "a"))
			require.NoError(t, s.ClaimNode(context.Background(), "PROJ-1", "agent-1"))
		}, false, model.StatusInProgress},
		{"deferred with no wake time", func(t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.DeferNode(context.Background(), "PROJ-1", nil, "d", "a"))
		}, false, model.StatusDeferred},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeferTestStore(t, model.StatusOpen)
			ctx := context.Background()
			tt.setup(t, s)
			before := deferEvents(t, s)

			woken, err := s.WakeDeferredNode(ctx, "PROJ-1", deferTestNow)
			require.NoError(t, err)

			assert.Equal(t, tt.wantWoken, woken)
			got, err := s.GetNode(ctx, "PROJ-1")
			require.NoError(t, err)
			assert.Equal(t, tt.wantStatus, got.Status)
			if !tt.wantWoken {
				assert.Equal(t, before, deferEvents(t, s), "a skipped node emits nothing")
				return
			}
			assert.Nil(t, got.DeferUntil)
			assert.Len(t, deferEvents(t, s), len(before)+1, "one transition_status for the wake")
		})
	}
}

// TestWakeDeferredNode_DeletedNode_IsSkipped verifies a node deleted after the
// pass selected it is skipped without an error (MTIX-95.22 round 3).
func TestWakeDeferredNode_DeletedNode_IsSkipped(t *testing.T) {
	s := newDeferTestStore(t, model.StatusOpen)
	ctx := context.Background()
	past := deferTestNow.Add(-time.Hour)
	require.NoError(t, s.DeferNode(ctx, "PROJ-1", &past, "d", "a"))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	woken, err := s.WakeDeferredNode(ctx, "PROJ-1", deferTestNow)
	require.NoError(t, err)
	assert.False(t, woken)
}
