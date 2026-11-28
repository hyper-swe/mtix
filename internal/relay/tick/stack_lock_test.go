// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package tick_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
	"github.com/stretchr/testify/require"
)

// TestStack_PublishLockIsSharedAcrossStacks: a publish lock held through
// the per-store lock file (another process, another stack) keeps this
// stack from publishing, and releasing it lets the next pass through.
func TestStack_PublishLockIsSharedAcrossStacks(t *testing.T) {
	r := newRelay(t, true)
	r.journal(t, 2)
	lockDir := t.TempDir()
	cfg := r.config()
	cfg.LockDir = lockDir
	stack, err := tick.Open(cfg)
	require.NoError(t, err)

	held, err := pushlock.AcquireNamed(lockDir, tick.PublishLockName)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	n, err := stack.Publisher.PublishPending(ctx)
	require.Zero(t, n)
	require.Error(t, err, "a publisher that cannot take the lock must not publish")
	require.NoError(t, held.Release())

	n, err = stack.Publisher.PublishPending(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, n)
}
