// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MTIX-245 story 2: the no-policy store wait also ends on the wall clock when
// it jumps past the timeout between ticks (a system sleep).
func TestInboxWait_WallClockJumpPastTimeout_ReturnsEmptyWithinOneTick(t *testing.T) {
	s, err := New(filepath.Join(t.TempDir(), "wall.db"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, s.Close()) })
	start := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	var calls atomic.Int64
	s.waitClock = func() time.Time {
		if calls.Add(1) <= 1 { // the entry reading that fixes the deadline
			return start
		}
		return start.Add(2 * time.Hour)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	began := time.Now()
	events, err := s.InboxWait(ctx, "w", time.Hour)
	require.NoError(t, err)
	assert.Empty(t, events)
	assert.Less(t, time.Since(began), 3*time.Second)
}
