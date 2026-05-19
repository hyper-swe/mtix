// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// These tests cover the internal helpers in sync_push.go / sync_pull.go /
// sync_clone.go that get bypassed by the e2e suite (which calls the
// store primitives directly). Direct unit tests close the coverage
// gap for QUALITY-STANDARDS §2.1 without requiring a live PG hub —
// the helpers work entirely against local SQLite. The push/pull
// LOOP functions (pushLoop / pullLoop / cloneLoop) need a pool and
// are exercised by the PG-gated e2e suite; the helpers below are
// the local-state pieces those loops invoke.

// --- sync_push.go helpers ---

func TestReadPendingBatch_EmptyStoreReturnsEmpty(t *testing.T) {
	initTestApp(t)
	events, err := readPendingBatch(context.Background(), app.store, 100)
	require.NoError(t, err)
	require.Empty(t, events, "fresh store has no pending events")
}

func TestReadPendingBatch_ReturnsPendingInLamportOrder(t *testing.T) {
	initTestApp(t)
	// CreateNode emits pending sync_events.
	require.NoError(t, runCreate("first", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("second", "", "", 3, "", "", "", "", ""))

	events, err := readPendingBatch(context.Background(), app.store, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(events), 2,
		"two creates should produce at least 2 pending events")

	// Lamport must be monotonic.
	var prev int64 = -1
	for _, e := range events {
		require.Greater(t, e.LamportClock, prev,
			"events must be returned in lamport-ascending order")
		prev = e.LamportClock
	}
}

func TestReadPendingBatch_LimitHonored(t *testing.T) {
	initTestApp(t)
	for i := 0; i < 5; i++ {
		require.NoError(t, runCreate("n"+string(rune('a'+i)), "", "", 3, "", "", "", "", ""))
	}
	events, err := readPendingBatch(context.Background(), app.store, 2)
	require.NoError(t, err)
	require.LessOrEqual(t, len(events), 2,
		"limit must be honored even when more events are available")
}

func TestMarkPushed_EmptyIDsIsNoop(t *testing.T) {
	initTestApp(t)
	err := markPushed(context.Background(), app.store, nil)
	require.NoError(t, err, "empty IDs is a clean no-op")
}

func TestMarkPushed_TransitionsPendingToPushed(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))
	ctx := context.Background()

	events, err := readPendingBatch(ctx, app.store, 100)
	require.NoError(t, err)
	require.NotEmpty(t, events)

	ids := make([]string, len(events))
	for i, e := range events {
		ids[i] = e.EventID
	}
	require.NoError(t, markPushed(ctx, app.store, ids))

	// After mark, readPendingBatch returns 0 pending.
	remaining, err := readPendingBatch(ctx, app.store, 100)
	require.NoError(t, err)
	require.Empty(t, remaining, "all events marked pushed; nothing pending")

	// Direct SQL check: sync_status='pushed' for each ID.
	for _, id := range ids {
		var status string
		require.NoError(t, app.store.QueryRow(ctx,
			`SELECT sync_status FROM sync_events WHERE event_id = ?`, id,
		).Scan(&status))
		require.Equal(t, string(model.SyncStatusPushed), status)
	}
}

// --- sync_pull.go helpers ---

func TestReadLastPulledClock_FreshStoreReturnsZero(t *testing.T) {
	initTestApp(t)
	cursor, err := readLastPulledClock(context.Background(), app.store)
	require.NoError(t, err)
	require.Equal(t, int64(0), cursor)
}

func TestWriteLastPulledClock_RoundTrip(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, writeLastPulledClock(ctx, app.store, 42))
	cursor, err := readLastPulledClock(ctx, app.store)
	require.NoError(t, err)
	require.Equal(t, int64(42), cursor)
}

func TestApplyPullBatch_EmptyBatchIsNoop(t *testing.T) {
	initTestApp(t)
	require.NoError(t, applyPullBatch(context.Background(), app.store, nil))
}

// --- sync_clone.go helpers ---

func TestLocalHasEvents_FreshStoreReturnsFalse(t *testing.T) {
	initTestApp(t)
	hasEvents, err := localHasEvents(context.Background(), app.store)
	require.NoError(t, err)
	require.False(t, hasEvents, "fresh store has no events")
}

func TestLocalHasEvents_AfterCreateReturnsTrue(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))
	hasEvents, err := localHasEvents(context.Background(), app.store)
	require.NoError(t, err)
	require.True(t, hasEvents, "after a create, the store has events")
}

func TestReadCloneCheckpoint_FreshStoreNoResumeReturnsZero(t *testing.T) {
	initTestApp(t)
	cursor, err := readCloneCheckpoint(context.Background(), app.store, false)
	require.NoError(t, err)
	require.Equal(t, int64(0), cursor)
}

func TestWriteCloneCheckpoint_RoundTrip(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, writeCloneCheckpoint(ctx, app.store, 123))
	cursor, err := readCloneCheckpoint(ctx, app.store, true)
	require.NoError(t, err)
	require.Equal(t, int64(123), cursor,
		"resume=true must surface the persisted checkpoint")
}

func TestReadCloneCheckpoint_ResumeFalseIgnoresPersisted(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, writeCloneCheckpoint(ctx, app.store, 500))
	cursor, err := readCloneCheckpoint(ctx, app.store, false)
	require.NoError(t, err)
	require.Equal(t, int64(0), cursor,
		"resume=false must start from 0 even when a checkpoint exists")
}

func TestApplyBatch_EmptyBatchIsNoop(t *testing.T) {
	initTestApp(t)
	require.NoError(t, applyBatch(context.Background(), app.store, nil))
}
