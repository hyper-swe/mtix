// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// PG-free tests of pullThenSweep (MTIX-95.5): the cursor pass, then the
// late-event sweep; and, when the cursor pass fails because an event's node
// is missing (a late push that straddles the cursor), the sweep and ONE
// retry of the cursor pass. The fake hub is fakeLateHub
// (sync_pull_sweep_test.go); its pullEvents serve the cursor pass and its
// events the sweep's listing and fetches. offlineEvents are a create (L1)
// and two edits (L2, L3) of TEST-9.

// TestPullThenSweep_CursorPassMissingNode_SweepsThenRetriesOnce: the cursor
// pass returns the two edits without their create; the sweep applies the
// create and the edits in Lamport order; the one retry of the cursor pass
// then succeeds from the same cursor, since the failed batch was rolled
// back.
func TestPullThenSweep_CursorPassMissingNode_SweepsThenRetriesOnce(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	hub := &fakeLateHub{events: events, pullEvents: events[1:], hubNows: []time.Time{sweepHubT1}}
	var stderr bytes.Buffer

	got, err := pullThenSweep(ctx, &stderr, hub, app.store, 0, 100)

	require.NoError(t, err)
	require.Equal(t, []int64{0, 0}, hub.pullCalls, "one failed cursor pass, one retry from the same cursor")
	require.Len(t, hub.calls, 1, "one sweep listing, before the retry")
	require.Equal(t, 3, got.sweep.Recovered)
	require.Equal(t, 2, got.pulled, "the retry pulls the two edits again (already applied)")
	require.Contains(t, stderr.String(), "retrying the pull once")
	cursor, err := readLastPulledClock(ctx, app.store)
	require.NoError(t, err)
	require.Equal(t, int64(3), cursor)
	node, err := app.store.GetNode(ctx, "TEST-9")
	require.NoError(t, err)
	require.Equal(t, "second edit", node.Description)
}

// TestPullThenSweep_Failures_RetryAtMostOnce: the cursor pass is retried at
// most once, and only after a missing-node failure and a successful sweep.
// A cursor pass that still fails after the retry returns its error; a
// sweep that fails returns its error without a retry; any other cursor-pass
// failure returns at once, without a sweep. A failed batch is rolled back:
// the cursor does not move and its events are not recorded as applied.
func TestPullThenSweep_Failures_RetryAtMostOnce(t *testing.T) {
	tests := []struct {
		name          string
		hub           func(events []*model.SyncEvent) *fakeLateHub
		wantStage     string
		wantErr       string
		wantPullCalls []int64
		wantListings  int
	}{
		{"create is nowhere on the hub: one retry, then the error", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{pullEvents: ev[1:], hubNows: []time.Time{sweepHubT1}}
		}, "pull loop", "not found", []int64{0, 0}, 1},
		{"the sweep itself fails: no retry", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{events: ev[1:], pullEvents: ev[1:], hubNows: []time.Time{sweepHubT1}}
		}, "late-event sweep", "not found", []int64{0}, 1},
		{"transport error: no sweep", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{events: ev, pullEvents: ev, pullErr: errors.New("connection reset"),
				hubNows: []time.Time{sweepHubT1}}
		}, "pull loop", "connection reset", []int64{0}, 0},
		{"apply error other than a missing node: no sweep", func(ev []*model.SyncEvent) *fakeLateHub {
			bad := *ev[0]
			bad.OpType = model.OpType("not_an_op")
			return &fakeLateHub{events: ev, pullEvents: []*model.SyncEvent{&bad},
				hubNows: []time.Time{sweepHubT1}}
		}, "pull loop", "not_an_op", []int64{0}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			events := offlineEvents(t)
			hub := tt.hub(events)

			got, err := pullThenSweep(ctx, &bytes.Buffer{}, hub, app.store, 0, 100)

			require.Error(t, err)
			require.Contains(t, err.Error(), tt.wantErr)
			require.Equal(t, tt.wantStage, got.stage)
			require.Equal(t, tt.wantPullCalls, hub.pullCalls)
			require.Len(t, hub.calls, tt.wantListings)
			cursor, err := readLastPulledClock(ctx, app.store)
			require.NoError(t, err)
			require.Zero(t, cursor, "a failed batch does not move the cursor")
			require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`,
				events[2].EventID), "a failed batch is rolled back")
		})
	}
}

// TestPullThenSweep_NoFailure_NoExtraHubQuery: a clean cursor pass is
// followed by exactly one sweep and no retry.
func TestPullThenSweep_NoFailure_NoExtraHubQuery(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	hub := &fakeLateHub{events: events, pullEvents: events, hubNows: []time.Time{sweepHubT1}}

	got, err := pullThenSweep(context.Background(), &bytes.Buffer{}, hub, app.store, 0, 100)

	require.NoError(t, err)
	require.Equal(t, []int64{0}, hub.pullCalls)
	require.Len(t, hub.calls, 1)
	require.Empty(t, hub.fetched, "the cursor pass already applied everything")
	require.Equal(t, 3, got.pulled)
	require.Equal(t, "late-event sweep", got.stage)
}
