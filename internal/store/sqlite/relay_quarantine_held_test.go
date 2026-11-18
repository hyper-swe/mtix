// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestRelayQuarantinedCreate_MatchesPeerNodeAndCreateOnly pins the lookup that
// holds a peer's follow-ups to a skipped task (MTIX-273): only a skipped
// creation, of that id, from that peer; the newest one when the peer repeated it.
func TestRelayQuarantinedCreate_MatchesPeerNodeAndCreateOnly(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	create := relayQuarantineRec(4, "2026-11-13T01:00:00Z")
	update := relayQuarantineRec(5, "2026-11-13T01:00:00Z")
	update.OpType, update.NodeID = "update_field", "PROJ-2"
	repeat := relayQuarantineRec(9, "2026-11-13T01:00:00Z")
	for _, r := range []sqlite.RelayQuarantineRecord{create, update, repeat} {
		require.NoError(t, s.RecordRelayQuarantine(ctx, r))
	}

	tests := []struct {
		name   string
		peer   string
		node   string
		wantRS uint64
		held   bool
	}{
		{"the skipped creation, newest first", "fedcba9876543210", "PROJ-1", 9, true},
		{"another task id", "fedcba9876543210", "PROJ-3", 0, false},
		{"a skipped update is not a creation", "fedcba9876543210", "PROJ-2", 0, false},
		{"another peer", "0123456789abcdef", "PROJ-1", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rs, held, err := s.RelayQuarantinedCreate(ctx, tt.peer, tt.node)
			require.NoError(t, err)
			require.Equal(t, tt.held, held)
			require.Equal(t, tt.wantRS, rs)
		})
	}
}

// A resolved row still holds: a settled parent does not release its children.
func TestRelayQuarantinedCreate_ResolvedRowStillHolds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	rec := relayQuarantineRec(4, "2026-11-13T01:00:00Z")
	require.NoError(t, s.RecordRelayQuarantine(ctx, rec))
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		ok, err := sqlite.ResolveRelayQuarantineTx(ctx, tx, rec.RelayQuarantineKey, sqlite.RelayResolution{
			Resolution: sqlite.RelayResolutionRecreate, Actor: "op", Reason: "r", RecreatedAs: "PROJ-8", At: time.Now(),
		})
		require.True(t, ok)
		return err
	}))
	rs, held, err := s.RelayQuarantinedCreate(ctx, rec.PeerID, "PROJ-1")
	require.NoError(t, err)
	require.True(t, held)
	require.Equal(t, uint64(4), rs)
}
