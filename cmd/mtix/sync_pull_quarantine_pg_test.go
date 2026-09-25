// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Real-Postgres test of the Lamport jump bound of `mtix sync pull`
// (MTIX-95.11 acceptance 2). Gated on MTIX_PG_TEST_DSN.

// TestRunSyncPull_ExtremeLamportHubEvent_QuarantinedNextPushSucceeds: a hub
// event stamped just below the FR-18.7 overflow guard passes the hub's
// push validation, so the hub holds it. The peer's pull quarantines it
// (it is far more than sync.max_lamport_jump above the peer's clock), the
// peer's clock and cursor stay where they were, and the peer's next push
// is accepted. Before this change the peer adopted the extreme clock, and
// its next push was refused: its new event was stamped at 2^53.
func TestRunSyncPull_ExtremeLamportHubEvent_QuarantinedNextPushSucceeds(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-5", "", 0, 5, "stamped at the edge")))
	pending, err := readPendingBatch(ctx, f.b, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	extreme := *pending[0]
	extreme.LamportClock = validator.MaxLamportClock - 1
	accepted, _, _, err := f.pool.PushEventsWithRenumbers(ctx, []*model.SyncEvent{&extreme})
	require.NoError(t, err)
	require.Equal(t, []string{extreme.EventID}, accepted, "precondition: the hub holds the extreme event")

	out, errOut := f.pullPeerStreams(t, 100)

	require.Contains(t, errOut, "quarantined event "+extreme.EventID)
	require.Contains(t, out, "quarantine: 1 pulled events held")
	require.Contains(t, quarantined(t)[extreme.EventID].Reason, "sync.max_lamport_jump")
	require.Zero(t, peerLamport(t), "the local clock does not move")
	require.Zero(t, f.peerCursor(t), "the cursor does not move to the refused clock")
	requireNotApplied(t, &extreme)

	require.NoError(t, runCreate("after the extreme event", "", "", 3, "", "", "", "", ""))
	f.pushPeer(t)
	var pendingAfter int
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`).Scan(&pendingAfter))
	require.Zero(t, pendingAfter, "the peer's next push was accepted")
	require.Equal(t, int64(1), peerLamport(t))
}

// TestRunSyncPull_CorruptVectorClockRow_QuarantinedPullContinues: a hub row
// whose vector_clock does not decode used to fail the whole PullEvents, so
// every pull stalled on it. The transport now returns it marked malformed
// and the pull quarantines it; the other events apply and the pull
// succeeds (MTIX-95.11 round 2). The test hub is a throwaway database.
func TestRunSyncPull_CorruptVectorClockRow_QuarantinedPullContinues(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-5", "", 0, 5, "row with a corrupt clock")))
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-6", "", 0, 6, "healthy row")))
	pushed := f.pushB(t)
	require.Len(t, pushed, 2)
	_, err := f.pool.Inner().Exec(ctx,
		`UPDATE sync_events SET vector_clock = '"not a map"'::jsonb WHERE event_id = $1`, pushed[0].EventID)
	require.NoError(t, err)

	out, errOut := f.pullPeerStreams(t, 100)

	require.Contains(t, errOut, "quarantined event "+pushed[0].EventID)
	require.Contains(t, out, "quarantine: 1 pulled events held")
	q := quarantined(t)[pushed[0].EventID]
	require.Contains(t, q.Reason, "vector_clock does not decode")
	require.Equal(t, "TEST-5", q.NodeID)
	_, err = app.store.GetNode(ctx, "TEST-6")
	require.NoError(t, err, "the healthy row applied")
	_, err = app.store.GetNode(ctx, "TEST-5")
	require.ErrorIs(t, err, model.ErrNotFound)
	require.Equal(t, pushed[1].LamportClock, f.peerCursor(t), "the cursor moves past the malformed row")
}
