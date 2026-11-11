// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/stretchr/testify/require"
)

// TestEmit_WallClockTS_FromInjectedClock: an emitted event's wall_clock_ts is
// read from the store's injected clock, never the process clock (MTIX-112.17.16.13;
// ADR-006 I1), so a replica's stored node times and its events agree.
func TestEmit_WallClockTS_FromInjectedClock(t *testing.T) {
	t.Parallel()
	s, raw := mutationTestStore(t)
	at := time.Date(2031, 5, 6, 7, 8, 9, 250_000_000, time.UTC)
	s.SetClock(func() time.Time { return at })
	ctx := context.Background()
	node := mustCreateNode(t, s, "MTIX-1", "")
	require.NoError(t, s.ClaimNode(ctx, "MTIX-1", "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, "MTIX-1", model.StatusDone, "", "agent-1"))

	rows, err := raw.Query(`SELECT op_type, wall_clock_ts FROM sync_events ORDER BY lamport_clock`)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	seen := map[string]int64{}
	for rows.Next() {
		var op string
		var ts int64
		require.NoError(t, rows.Scan(&op, &ts))
		seen[op] = ts
	}
	require.NoError(t, rows.Err())
	require.Len(t, seen, 3, "create_node, claim and transition_status are emitted")
	// create_node carries the node's own created_at, which the caller stamped.
	require.Equal(t, node.CreatedAt.UnixMilli(), seen[string(model.OpCreateNode)])
	require.Equal(t, at.UnixMilli(), seen[string(model.OpClaim)])
	require.Equal(t, at.UnixMilli(), seen[string(model.OpTransitionStatus)])
}
