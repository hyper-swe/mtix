// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests observed scan floors and bootstrap reads across journal replacement.
package sqlite_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createHookFloorNode(t *testing.T, s *sqlite.Store, id string) {
	t.Helper()
	require.NoError(t, s.CreateNode(context.Background(), makeRootNode(id, "TEST", "floor event", time.Now().UTC())))
}

func replacedHookFloorSnapshot(t *testing.T, dropped int) (*sqlite.Store, int64, int64) {
	t.Helper()
	ctx := context.Background()
	s, _ := retentionStore(t)
	createHookFloorNode(t, s, "TEST-1")
	board, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	for i := range dropped {
		createHookFloorNode(t, s, fmt.Sprintf("TEST-%d", i+2))
	}
	oldTail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, oldTail))
	observed, err := s.HookCursor(ctx)
	require.NoError(t, err)
	_, err = s.Import(ctx, board, sqlite.ImportModeReplace, false)
	require.NoError(t, err)
	surviving, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(dropped), observed-surviving, "fixture must rewind by the exact event count")
	for i := range dropped + 1 {
		createHookFloorNode(t, s, fmt.Sprintf("TEST-%d", i+dropped+2))
	}
	return s, observed, surviving
}

func TestHookFence_ObservedFloorRejectsOneAndTwoEventRewinds(t *testing.T) {
	for _, dropped := range []int{1, 2} {
		t.Run(fmt.Sprintf("dropped=%d", dropped), func(t *testing.T) {
			ctx := context.Background()
			s, observed, surviving := replacedHookFloorSnapshot(t, dropped)
			page, err := s.ReadJournalSince(ctx, observed, 500)
			require.NoError(t, err)
			require.NotEmpty(t, page)
			require.Greater(t, page[0].Seq, surviving+1, "the stale read omitted newly reused event numbers")
			misses, err := s.AdvanceHookScanFloorForEvents(ctx, observed, page)
			require.NoError(t, err)
			assert.Equal(t, uint64(1), misses)
			floor, err := s.HookCursor(ctx)
			require.NoError(t, err)
			assert.Equal(t, surviving, floor)
			unseen, err := s.ReadJournalSince(ctx, floor, 500)
			require.NoError(t, err)
			require.NotEmpty(t, unseen)
			assert.Equal(t, surviving+1, unseen[0].Seq)
		})
	}
}

func TestHookLedger_BootstrapFloorUsesWriterSnapshot(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	createHookFloorNode(t, s, "TEST-1")
	createHookFloorNode(t, s, "TEST-2")
	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	// Bootstrap floor initialization is a writer operation. A separate read
	// pool must not supply the tail that its write transaction consumes.
	require.NoError(t, s.ReadDB().Close())
	require.NoError(t, s.InitHookScanFloorAtTail(ctx))
	var floor int64
	require.NoError(t, s.WriteDB().QueryRow(`SELECT cursor FROM hook_dispatch_cursor WHERE id=1`).Scan(&floor))
	assert.Equal(t, tail, floor)
}

func TestHookLedger_BootstrapReadFailurePreservesCursor(t *testing.T) {
	ctx := context.Background()
	s, _ := retentionStore(t)
	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, 3))
	_, err := s.WriteDB().Exec(`DROP TABLE sync_events`)
	require.NoError(t, err)
	require.Error(t, s.InitHookScanFloorAtTail(ctx))
	var floor int64
	require.NoError(t, s.WriteDB().QueryRow(`SELECT cursor FROM hook_dispatch_cursor WHERE id=1`).Scan(&floor))
	assert.Equal(t, int64(3), floor)
}
