// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Resolution readers and acknowledgments reject absent connections cleanly.
func TestCollisionResolution_NoPool(t *testing.T) {
	for _, pool := range []*transport.Pool{nil, {}} {
		_, _, _, err := pool.GetCollision(context.Background(), 1)
		require.ErrorContains(t, err, "pool not open")
		require.ErrorContains(t, pool.CompleteCollisionMove(context.Background(), 1, "winner", "TEST-2"), "pool not open")
	}
}

// The accepted winner and completion survive separate connections/retries;
// an opposite choice or a different completion cannot overwrite them.
func TestCollisionResolution_AcceptAndComplete(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))
	held := uidEvent("0193fa00-0000-7000-8000-0000000a3001", "TEST-1", "alice", 1)
	held.ProjectPrefix = "TEST"
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{held})
	require.NoError(t, err)
	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)
	incoming := uidEvent("0193fa00-0000-7000-8000-0000000a3002", "TEST-1", "bob", 2)
	incoming.ProjectPrefix = "TEST"
	_, _, _, collisions, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{incoming})
	require.NoError(t, err)
	require.Len(t, collisions, 1)
	rows, err := pool.ListOpenCollisions(ctx, "TEST")
	require.NoError(t, err)
	require.Len(t, rows, 1)
	id := rows[0].CollisionID
	accepted, err := pool.ResolveCollision(ctx, id, held.EventID, "", "operator")
	require.NoError(t, err)
	require.True(t, accepted)
	c, winner, path, err := pool.GetCollision(ctx, id)
	require.NoError(t, err)
	require.Equal(t, rows[0], c)
	require.Equal(t, held.EventID, winner)
	require.Empty(t, path)
	accepted, err = pool.ResolveCollision(ctx, id, incoming.EventID, "", "other")
	require.NoError(t, err)
	require.False(t, accepted)
	require.Error(t, pool.CompleteCollisionMove(ctx, id, incoming.EventID, "TEST-2"))
	require.NoError(t, pool.CompleteCollisionMove(ctx, id, held.EventID, "TEST-2"))
	require.NoError(t, pool.CompleteCollisionMove(ctx, id, held.EventID, "TEST-2"))
	require.Error(t, pool.CompleteCollisionMove(ctx, id, held.EventID, "TEST-3"))
	_, _, path, err = pool.GetCollision(ctx, id)
	require.NoError(t, err)
	require.Equal(t, "TEST-2", path)
	missing, _, _, err := pool.GetCollision(ctx, -1)
	require.NoError(t, err)
	require.Zero(t, missing.CollisionID)
}
