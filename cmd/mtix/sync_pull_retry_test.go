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

// PG-free tests of pullThenSweep (MTIX-95.5, MTIX-95.11): the cursor pass,
// then the late-event sweep, then, when either applied an event, a retry
// of the quarantine. A late push that straddles the cursor (a node's
// create below it, edits above it) no longer fails the cursor pass: the
// edits are quarantined, the sweep delivers the create, and the retry at
// the end of the same pull applies the edits. The fake hub is fakeLateHub
// (sync_pull_sweep_test.go); its pullEvents serve the cursor pass and its
// events the sweep's listing and fetches. offlineEvents are a create (L1)
// and two edits (L2, L3) of TEST-9.

// TestPullThenSweep_CursorPassMissingNode_QuarantinedThenAppliedSamePull:
// the cursor pass returns the two edits without their create; both are
// quarantined and the cursor moves past them; the sweep fetches only the
// create (the quarantine holds the edits) and applies it; the retry at the
// end of the pull applies the edits in Lamport order. There is one cursor
// pass and no error.
func TestPullThenSweep_CursorPassMissingNode_QuarantinedThenAppliedSamePull(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	hub := &fakeLateHub{events: events, pullEvents: events[1:], hubNows: []time.Time{sweepHubT1}}
	var stderr bytes.Buffer

	got, err := pullThenSweep(ctx, testIngest(&stderr), hub, app.store, 0, 100)

	require.NoError(t, err)
	require.Equal(t, []int64{0}, hub.pullCalls, "one cursor pass")
	require.Len(t, hub.calls, 1, "one sweep listing")
	require.Equal(t, [][]string{{events[0].EventID}}, hub.fetched, "the sweep fetches only the create")
	require.Zero(t, got.pulled, "the cursor pass applied nothing")
	require.Equal(t, 1, got.sweep.Recovered)
	require.Equal(t, 2, got.retried, "the end-of-pull retry applied both edits")
	require.Contains(t, stderr.String(), "quarantined event "+events[1].EventID)
	require.Contains(t, stderr.String(), "2 quarantined events applied on retry; 0 still quarantined")
	cursor, err := readLastPulledClock(ctx, app.store)
	require.NoError(t, err)
	require.Equal(t, int64(3), cursor)
	node, err := app.store.GetNode(ctx, "TEST-9")
	require.NoError(t, err)
	require.Equal(t, "second edit", node.Description)
	require.Empty(t, quarantined(t))
}

// TestPullThenSweep_CreateNowhere_EditsStayQuarantined: when the create is
// on no page of the hub, the pull still succeeds; the edits stay
// quarantined for a later pull, and no retry runs because nothing applied.
func TestPullThenSweep_CreateNowhere_EditsStayQuarantined(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	hub := &fakeLateHub{pullEvents: events[1:], hubNows: []time.Time{sweepHubT1}}

	got, err := pullThenSweep(ctx, testIngest(nil), hub, app.store, 0, 100)

	require.NoError(t, err)
	require.Zero(t, got.pulled)
	require.Zero(t, got.retried)
	q := quarantined(t)
	require.Len(t, q, 2)
	require.Equal(t, 1, q[events[1].EventID].Attempts, "no end-of-pull retry after a pull that applied nothing")
	require.Contains(t, q[events[1].EventID].Reason, "not found")
	cursor, err := readLastPulledClock(ctx, app.store)
	require.NoError(t, err)
	require.Equal(t, int64(3), cursor, "the cursor moves past quarantined events")
}

// TestPullThenSweep_Failures_ReturnTheirStage: a hub failure still fails
// the pull, with the stage that failed; a failure of the cursor pass stops
// before the sweep, and a failed sweep keeps what the cursor pass applied.
func TestPullThenSweep_Failures_ReturnTheirStage(t *testing.T) {
	tests := []struct {
		name          string
		hub           func(events []*model.SyncEvent) *fakeLateHub
		wantStage     string
		wantErr       string
		wantListings  int
		wantCursor    int64
		wantPullCalls []int64
	}{
		{"transport error: no sweep", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{events: ev, pullEvents: ev, pullErr: errors.New("connection reset"),
				hubNows: []time.Time{sweepHubT1}}
		}, "pull loop", "connection reset", 0, 0, []int64{0}},
		{"sweep listing fails after the cursor pass", func(ev []*model.SyncEvent) *fakeLateHub {
			return &fakeLateHub{events: ev, pullEvents: ev, listErr: errors.New("listing timed out"),
				hubNows: []time.Time{sweepHubT1}}
		}, "late-event sweep", "listing timed out", 1, 3, []int64{0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			hub := tt.hub(offlineEvents(t))

			got, err := pullThenSweep(ctx, testIngest(nil), hub, app.store, 0, 100)

			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantStage, got.stage)
			require.Equal(t, tt.wantPullCalls, hub.pullCalls)
			require.Len(t, hub.calls, tt.wantListings)
			cursor, err := readLastPulledClock(ctx, app.store)
			require.NoError(t, err)
			require.Equal(t, tt.wantCursor, cursor)
		})
	}
}

// TestPullThenSweep_NoFailure_NoExtraHubQuery: a clean cursor pass is
// followed by exactly one sweep, and the quarantine retry adds no hub
// query.
func TestPullThenSweep_NoFailure_NoExtraHubQuery(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	hub := &fakeLateHub{events: events, pullEvents: events, hubNows: []time.Time{sweepHubT1}}

	got, err := pullThenSweep(context.Background(), testIngest(nil), hub, app.store, 0, 100)

	require.NoError(t, err)
	require.Equal(t, []int64{0}, hub.pullCalls)
	require.Len(t, hub.calls, 1)
	require.Empty(t, hub.fetched, "the cursor pass already applied everything")
	require.Equal(t, 3, got.pulled)
	require.Zero(t, got.retried)
	require.Equal(t, "late-event sweep", got.stage)
}
