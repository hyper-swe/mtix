// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// storeSyncs fails closed and uses the caller's context: a store that cannot
// be read is an error carrying its cause, not "does not sync" (MTIX-107.31).
func TestStoreSyncs_CancelledContext_ReturnsWrappedError(t *testing.T) {
	st, err := sqlite.New(filepath.Join(t.TempDir(), "sync.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	s := NewSyncService(st, slog.Default(), time.Now)

	synced, err := s.storeSyncs(context.Background())
	require.NoError(t, err)
	assert.False(t, synced, "a new store does not sync")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = s.storeSyncs(ctx)
	require.ErrorIs(t, err, context.Canceled)
	assert.Contains(t, err.Error(), "whether the store syncs with a hub")
}
