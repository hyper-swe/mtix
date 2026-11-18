// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
)

// publishWithFutureRecord publishes one segment: PROJ-1, a PROJ-2 stamped just
// past the 24h forward window (refused now, valid in a few seconds), PROJ-3.
func (r *peerRig) publishWithFutureRecord(t *testing.T) {
	t.Helper()
	e1 := event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "n1"})
	e2 := event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "n2"})
	e2.WallClockTS = time.Now().Add(24*time.Hour + 2*time.Second).UnixMilli()
	e3 := event(t, model.OpCreateNode, "PROJ-3", 3, &model.CreateNodePayload{Title: "n3"})
	r.publish(t, fleetKey, marshalEvents(t, e1, e2, e3)...)
}

// TestRelayIngest_CloneQuarantinedRecordStaysOutOnLaterPolls is the MTIX-265
// review probe: a clone seeded with a stamped epoch must move its cursor on the
// first poll exactly as base did, so a record quarantined on poll 1 is not
// re-judged (and applied behind an open quarantine row) on poll 2.
func TestRelayIngest_CloneQuarantinedRecordStaysOutOnLaterPolls(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()
	r.publishWithFutureRecord(t)
	r.seedCloneEpoch(t, 3, 1)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, stats.Quarantined)
	require.Equal(t, 2, stats.Applied)
	require.False(t, r.hasNode(t, "PROJ-2"))
	require.Greater(t, r.cursor(t).SegmentNo, uint64(0), "the cursor left segment 0 on the first poll")

	time.Sleep(3 * time.Second) // the record's timestamp is now inside the window
	stats, err = in.IngestAll(ctx)
	require.NoError(t, err)
	require.Zero(t, stats.Applied, "poll 2 re-reads nothing")
	require.Zero(t, stats.Quarantined)
	require.False(t, r.hasNode(t, "PROJ-2"), "a quarantined record is never applied behind its open row")
	rows, err := r.store.ListRelayQuarantine(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].Attempts, "the attempt count does not grow on later polls")
}

// TestRelayIngest_CloneIdlePeerIsNotReReadOnLaterPolls: an idle peer's segments
// are read once; polls 2 and 3 apply, quarantine and stall nothing and leave
// the attempt count alone.
func TestRelayIngest_CloneIdlePeerIsNotReReadOnLaterPolls(t *testing.T) {
	r := newPeerRig(t)
	ctx := context.Background()
	collidingCreate(t, r)
	r.publishNodes(t, fleetKey, 5, 2)
	r.seedCloneEpoch(t, 3, 1)

	in, err := ingest.New(r.config())
	require.NoError(t, err)
	first, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, first.Quarantined)
	require.Equal(t, 2, first.Applied)
	moved := r.cursor(t)
	require.Greater(t, moved.SegmentNo, uint64(0))

	for poll := 2; poll <= 3; poll++ {
		stats, err := in.IngestAll(ctx)
		require.NoError(t, err)
		require.Zero(t, stats.Applied, "poll %d", poll)
		require.Zero(t, stats.Quarantined, "poll %d", poll)
		require.Empty(t, stats.Stalls, "poll %d", poll)
		require.Equal(t, moved, r.cursor(t), "poll %d", poll)
	}
	rows, err := r.store.ListRelayQuarantine(ctx)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[0].Attempts)
}
