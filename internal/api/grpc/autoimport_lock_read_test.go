// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// gRPC reads use the cache after CLI startup; handlers do not auto-import.
// Native tasks.json ownership must not prevent database reads (FR-15.8, MTIX-149).
func TestGRPCRead_SyncLockHeld_ReturnsNodeData(t *testing.T) {
	s := testGRPCServer(t)
	node := createTestNode(t, s, "gRPC cached node", "TEST")
	dir := t.TempDir()
	syncSvc := service.NewSyncService(s.store, s.logger, testClock())
	require.NoError(t, syncSvc.ForceExport(t.Context(), dir))
	lock, err := pushlock.AcquireNamed(dir, "sync.lock")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, lock.Release()) })
	contender, err := pushlock.AcquireNamed(dir, "sync.lock")
	require.ErrorIs(t, err, pushlock.ErrLockHeld)
	require.Nil(t, contender)

	got, err := s.HandleGetNode(t.Context(), node.ID)
	require.NoError(t, err, "busy file ownership must not become a gRPC status error")
	require.NotNil(t, got)
	require.Equal(t, node.ID, got.ID)
	require.Equal(t, node.Title, got.Title)
	require.Equal(t, node.Status, got.Status)
}
