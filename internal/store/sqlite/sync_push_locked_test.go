// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestWithPushLock_ReadsAndHoldsInOneTransaction (MTIX-95.39): inside the
// lock push reads the held creations and the batch's tasks, records holds
// and counts them, all in one write transaction; a held row is visible to
// the lock's own reads before the commit and to the store after it.
func TestWithPushLock_ReadsAndHoldsInOneTransaction(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	err := s.WithPushLock(ctx, func(l sqlite.PushLocked) error {
		require.False(t, l.Now().IsZero())
		before, err := l.CountHeldPushEvents(ctx)
		require.NoError(t, err)
		require.Zero(t, before)
		require.NoError(t, l.HoldPushEvents(ctx, nil, "v"), "no holds is a no-op")
		require.NoError(t, l.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold("held-1"), pushHold("held-2")}, "v"))
		after, err := l.CountHeldPushEvents(ctx)
		require.NoError(t, err)
		require.Equal(t, 2, after, "the lock sees its own holds")
		creations, err := l.HeldPushCreations(ctx)
		require.NoError(t, err)
		require.Empty(t, creations, "holds with no creation event row are not creations")
		subjects, err := l.PushSubjects(ctx, []string{"held-1"})
		require.NoError(t, err)
		require.Empty(t, subjects)
		version, err := l.DataVersion(ctx)
		require.NoError(t, err)
		require.NotZero(t, version)
		return nil
	})
	require.NoError(t, err)
	n, err := s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, n, "the holds are committed")
}

// TestWithPushLock_ErrorRollsBackHolds: an error from the locked function
// leaves no hold behind.
func TestWithPushLock_ErrorRollsBackHolds(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	boom := errors.New("boom")

	err := s.WithPushLock(ctx, func(l sqlite.PushLocked) error {
		require.NoError(t, l.HoldPushEvents(ctx, []sqlite.QuarantinedEvent{pushHold("held-1")}, "v"))
		return boom
	})
	require.ErrorIs(t, err, boom)
	n, err := s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}
