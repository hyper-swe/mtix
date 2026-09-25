// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Real-Postgres tests of the clone's checks (MTIX-95.11 round 3). Gated on
// MTIX_PG_TEST_DSN.

// cloneRecoveryText is the recovery a clone refusal gives (MTIX-95.11.1
// round 2): on a fresh store, pull alone; discard-local only on a store
// that already holds sync state, after a push, a pending count of 0 and a
// human's go-ahead, since it deletes local tasks and unpushed changes.
const cloneRecoveryText = "Clone has no quarantine: on a fresh store, run 'mtix sync pull' instead, which " +
	"quarantines the event and applies the rest. Only on a store that already holds sync state does " +
	"'mtix sync reconcile --discard-local --yes' come first; it deletes local tasks and unpushed changes, so " +
	"before it run 'mtix sync push', check that 'mtix sync status' shows pending 0, and get a human's go-ahead"

// peerState is the local state a refused clone must leave unchanged.
type peerState struct {
	lamport, cursor                    int64
	events, applied, nodes, quarantine int
	lastSweep                          string
}

// readPeerState reads the peer store's clock, cursor, row counts and sweep
// time.
func readPeerState(t *testing.T, f *sweepFixture) peerState {
	t.Helper()
	return peerState{
		lamport: peerLamport(t), cursor: f.peerCursor(t),
		events:     countTestRows(t, `SELECT COUNT(*) FROM sync_events`),
		applied:    countTestRows(t, `SELECT COUNT(*) FROM applied_events`),
		nodes:      countTestRows(t, `SELECT COUNT(*) FROM nodes`),
		quarantine: countTestRows(t, `SELECT COUNT(*) FROM sync_quarantine`),
		lastSweep:  f.peerMeta(t, "meta.sync.last_sweep_at"),
	}
}

// pushExtremeFromB pushes, from B, a create and a second create stamped
// just below the FR-18.7 overflow guard, which the hub accepts.
func pushExtremeFromB(t *testing.T, f *sweepFixture) (ok, extreme *model.SyncEvent) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-4", "", 0, 4, "ordinary")))
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-5", "", 0, 5, "stamped at the edge")))
	pending, err := readPendingBatch(ctx, f.b, 10)
	require.NoError(t, err)
	require.Len(t, pending, 2)
	e := *pending[1]
	e.LamportClock = validator.MaxLamportClock - 2
	accepted, _, _, err := f.pool.PushEventsWithRenumbers(ctx, []*model.SyncEvent{pending[0], &e})
	require.NoError(t, err)
	require.Len(t, accepted, 2, "precondition: the hub holds both")
	return pending[0], &e
}

// TestRunSyncClone_ExtremeLamportHubRow_RefusedNothingWritten: a hub row at
// Lamport 2^53-2 makes clone refuse before it writes anything: the local
// clock, cursor, tables and sweep state are unchanged, and the refusal
// names the event and the recovery. With --batch-size 1 the ordinary event
// is a batch of its own, so a clone that checked only while applying would
// have written it. The recovery it names, a pull,
// quarantines the event and applies the rest.
func TestRunSyncClone_ExtremeLamportHubRow_RefusedNothingWritten(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	ok, extreme := pushExtremeFromB(t, f)
	quarantineN(t, 1)
	setLastSweep(t, "2026-09-24T09:00:00Z")
	before := readPeerState(t, f)
	require.Equal(t, 1, before.quarantine, "precondition: a quarantine row is seeded")
	var stdout, stderr bytes.Buffer

	err := runSyncClone(ctx, &stdout, &stderr, []string{f.dsn}, transport.Options{InsecureTLS: true}, false, 1)

	require.Error(t, err)
	require.Equal(t, fmt.Sprintf("mtix sync clone check: clone refused: hub event %s fails the checks sync pull runs "+
		"(lamport_clock %d is %d above the local clock %d (sync.max_lamport_jump %d): lamport_clock jump beyond "+
		"sync.max_lamport_jump); nothing was written. "+cloneRecoveryText,
		extreme.EventID, extreme.LamportClock, extreme.LamportClock-ok.LamportClock, ok.LamportClock,
		validator.DefaultMaxLamportJump), err.Error(), "the exact refusal, recovery included")
	require.Equal(t, before, readPeerState(t, f),
		"a refused clone writes nothing: clock, cursor, rows, the seeded quarantine row and the sweep time")
	require.NotContains(t, stdout.String(), "clone complete")

	out, _ := f.pullPeerStreams(t, 100)

	require.Contains(t, out, "quarantine: 2 pulled events held", "the seeded row and the extreme event")
	require.True(t, f.appliedOnPeer(t, ok.EventID))
	require.False(t, f.appliedOnPeer(t, extreme.EventID))
	require.Less(t, peerLamport(t), int64(1)<<32, "the extreme clock was never adopted")
}

// TestRunSyncClone_CorruptVectorClockRow_RefusedQuotingMalformed: a hub row
// whose vector_clock does not decode refuses the clone with the decode
// note, not a misleading "vector_clock required".
func TestRunSyncClone_CorruptVectorClockRow_RefusedQuotingMalformed(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-5", "", 0, 5, "row with a corrupt clock")))
	pushed := f.pushB(t)
	_, err := f.pool.Inner().Exec(ctx,
		`UPDATE sync_events SET vector_clock = '"not a map"'::jsonb WHERE event_id = $1`, pushed[0].EventID)
	require.NoError(t, err)
	before := readPeerState(t, f)

	err = runSyncClone(ctx, &bytes.Buffer{}, &bytes.Buffer{}, []string{f.dsn},
		transport.Options{InsecureTLS: true}, false, 100)

	require.Error(t, err)
	require.Contains(t, err.Error(), pushed[0].EventID)
	require.Contains(t, err.Error(), "vector_clock does not decode")
	require.NotContains(t, err.Error(), "vector_clock required")
	require.Equal(t, before, readPeerState(t, f))
}
