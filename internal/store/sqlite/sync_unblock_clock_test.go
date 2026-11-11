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
// cancel, a cascade cancel, a dependency removal and a RefreshBlocked. Each
// case also pins the dependent's updated_at to the injected clock.
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
		{"dependency removal", "MTIX-1", func(ctx context.Context, t *testing.T, s *sqlite.Store) {
			require.NoError(t, s.RemoveDependency(ctx, "MTIX-1", "MTIX-2", model.DepTypeBlocks))
		}},
		{"refresh blocked", "MTIX-1", func(ctx context.Context, t *testing.T, s *sqlite.Store) {
			// Leave MTIX-2 sticky-blocked with no blocker row, as a missed
			// recompute would, then ask for the manual refresh.
			_, err := s.WriteDB().ExecContext(ctx,
				`DELETE FROM dependencies WHERE from_id = 'MTIX-1' AND to_id = 'MTIX-2'`)
			require.NoError(t, err)
			require.NoError(t, s.RefreshBlocked(ctx, "MTIX-2"))
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

			var updatedAt string
			require.NoError(t, raw.QueryRow(
				`SELECT updated_at FROM nodes WHERE id = 'MTIX-2'`).Scan(&updatedAt))
			require.Equal(t, resolveAt.UTC().Format(time.RFC3339), updatedAt,
				"the unblocked dependent's updated_at comes from the injected clock")
		})
	}
}

// TestAutoBlock_AddDependency_FromInjectedClock: the auto-block that a new
// blocks dependency applies to its target stamps the target's updated_at from
// the store's injected clock, never the process clock (MTIX-112.17.16.13;
// ADR-006 I1), the same rule the auto-unblock cases above pin.
func TestAutoBlock_AddDependency_FromInjectedClock(t *testing.T) {
	t.Parallel()
	s, raw := mutationTestStore(t)
	ctx := context.Background()
	setup := time.Date(2031, 5, 6, 7, 8, 9, 0, time.UTC)
	s.SetClock(func() time.Time { return setup })
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-2", "")
	linkAt := time.Date(2032, 6, 7, 8, 9, 10, 0, time.UTC)
	s.SetClock(func() time.Time { return linkAt })
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{FromID: "MTIX-1", ToID: "MTIX-2", DepType: model.DepTypeBlocks}))

	var status, updatedAt string
	require.NoError(t, raw.QueryRow(
		`SELECT status, updated_at FROM nodes WHERE id = 'MTIX-2'`).Scan(&status, &updatedAt))
	require.Equal(t, string(model.StatusBlocked), status)
	require.Equal(t, linkAt.UTC().Format(time.RFC3339), updatedAt,
		"the auto-blocked target's updated_at comes from the injected clock")
}
