// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// TestEmit_AutoUnblock_FromInjectedClock: when a local write resolves a
// blocker, the transition_status event that restores the blocked dependent
// reads wall_clock_ts from the store's injected clock, never the process
// clock (MTIX-112.17.16.13; ADR-006 I1). Covers the done transition, a plain
// cancel and a cascade cancel.
func TestEmit_AutoUnblock_FromInjectedClock(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		blocker string
		resolve func(ctx context.Context, t *testing.T, s *sqlite.Store)
	}{
		{"done transition", "MTIX-1", func(ctx context.Context, t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-1"))
			require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "", "agent-1"))
		}},
		{"cancel", "MTIX-1", func(ctx context.Context, t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "no longer needed", "agent-1", false))
		}},
		{"cascade cancel", "MTIX-1.1", func(ctx context.Context, t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.CancelNode(ctx, "MTIX-1", "no longer needed", "agent-1", true))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, raw := mutationTestStore(t)
			ctx := context.Background()
			setup := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
			s.SetClock(func() time.Time { return setup })
			mustCreateNode(t, s, "MTIX-1", "")
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			mustCreateNode(t, s, "MTIX-2", "")
			require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: tt.blocker, ToID: "MTIX-2", DepType: model.DepTypeBlocks}))
			resolveAt := time.Date(2032, 6, 7, 8, 9, 10, 250_000_000, time.UTC)
			s.SetClock(func() time.Time { return resolveAt })
			tt.resolve(ctx, t, s)

			var wall int64
			require.NoError(t, raw.QueryRow(
				`SELECT wall_clock_ts FROM sync_events
				  WHERE node_id = 'MTIX-2' AND op_type = ? AND payload LIKE '%"from":"blocked"%'`,
				string(model.OpTransitionStatus)).Scan(&wall), "the auto-unblock event is emitted")
			require.Equal(t, resolveAt.UnixMilli(), wall)
		})
	}
}
