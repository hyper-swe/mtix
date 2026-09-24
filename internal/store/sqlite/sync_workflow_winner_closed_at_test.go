// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/stretchr/testify/require"
)

// closed_at range regressions for MTIX-95.10 (review round 1, S1).
//
// A terminal winner stamps closed_at from the author's wall_clock_ts, and
// envelope validation rejects only negative values. RFC3339 has four-digit
// years, so a wall_clock_ts in year 10000 or later formats to a string that
// no reader can parse: GetNode fails, and ListNodes fails for the whole list,
// on every replica. Outside years 1..9999 closed_at falls back to the apply
// time, the value it had before MTIX-95.10, and the event still applies.

// year10000MS is 10000-01-01T00:00:00Z in Unix milliseconds, the first
// instant RFC3339 cannot represent.
const year10000MS = int64(253402300800000)

// TestApply_TerminalWinnerWallClockOutOfRange_ClosedAtFallsBackToApplyTime
// applies a winning done whose wall_clock_ts is beyond year 9999 and checks
// that the node stays readable, alone and in a list.
func TestApply_TerminalWinnerWallClockOutOfRange_ClosedAtFallsBackToApplyTime(t *testing.T) {
	tests := []struct {
		name string
		wall int64
	}{
		{"year 10000", year10000MS},
		{"largest wall_clock_ts", math.MaxInt64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s, raw := replicaWithNode(t)
			done := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
				transition(model.StatusInProgress, model.StatusDone), 2, "")
			done.WallClockTS = tt.wall

			pullEvents(t, s, []*model.SyncEvent{done})

			n, err := s.GetNode(ctx, "MTIX-1")
			require.NoError(t, err, "GetNode must still read the node")
			require.Equal(t, model.StatusDone, n.Status, "the event still applies")
			require.NotNil(t, n.ClosedAt)
			require.WithinDuration(t, time.Now().UTC(), *n.ClosedAt, time.Hour,
				"closed_at falls back to the apply time")
			row := nodeRow(t, raw, "MTIX-1")
			require.Equal(t, row["updated_at"], row["closed_at"],
				"the fallback is the same apply time as updated_at")

			nodes, _, err := s.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 10})
			require.NoError(t, err, "ListNodes must still read every node")
			require.Len(t, nodes, 1)
		})
	}
}

// TestApply_TerminalWinnerWallClockAtYear9999_ClosedAtKeptAsIs pins the upper
// bound: the last representable second is stored exactly, not replaced.
func TestApply_TerminalWinnerWallClockAtYear9999_ClosedAtKeptAsIs(t *testing.T) {
	ctx := context.Background()
	s, raw := replicaWithNode(t)
	done := foreignWorkflowEvent(t, "MTIX-1", model.OpTransitionStatus,
		transition(model.StatusInProgress, model.StatusDone), 2, "")
	done.WallClockTS = year10000MS - 1 // 9999-12-31T23:59:59.999Z

	pullEvents(t, s, []*model.SyncEvent{done})

	require.Equal(t, "9999-12-31T23:59:59Z", nodeRow(t, raw, "MTIX-1")["closed_at"])
	n, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.NotNil(t, n.ClosedAt)
	require.Equal(t, time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), n.ClosedAt.UTC())
}
