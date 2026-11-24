// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/stretchr/testify/require"
)

func applyRelay(t *testing.T, s *sqlite.Store, e *model.SyncEvent) error {
	t.Helper()
	return s.WithTx(t.Context(), func(tx *sql.Tx) error { return s.ApplyRelayEvent(t.Context(), tx, e) })
}

// Two relay peers that took the same number: the second creation is refused
// at ingest (the relay quarantine and its recreate fix take it), so the
// gateway never queues an envelope for a task it does not hold (FR-21 §11.7).
func TestRelayForward_SecondCreateAtHeldNumberIsRefusedNotQueued(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	a := hubCreateOf(t, "MTIX-1", clock.MustNewEventID(), "first peer")
	b := hubCreateOf(t, "MTIX-1", clock.MustNewEventID(), "other peer")
	b.AuthorID, b.AuthorMachineHash = "peer-c", "cccccccccccccccc"
	require.NoError(t, applyRelay(t, s, a))
	require.ErrorIs(t, applyRelay(t, s, b), sqlite.ErrNumberHeldByPendingCreate)
	pending, err := s.ReadPendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, a.EventID, pending[0].EventID)
	hub := newFakePushHub()
	_, err = pushLoop(ctx, io.Discard, hub, s)
	require.NoError(t, err)
	require.Len(t, hub.events, 1)
}

// The same number held by a task the hub already delivered is refused too,
// rather than counted as applied with no node behind it.
func TestRelayForward_CreateOnHubHeldNumberIsRefusedNotQueued(t *testing.T) {
	s := movesTestStore(t)
	h := hubCreateOf(t, "MTIX-1", clock.MustNewEventID(), "hub task")
	require.NoError(t, s.WithTx(t.Context(), func(tx *sql.Tx) error { return s.IdempotentApply(t.Context(), tx, h) }))
	b := hubCreateOf(t, "MTIX-1", clock.MustNewEventID(), "relay task")
	require.ErrorIs(t, applyRelay(t, s, b), sqlite.ErrNumberHeldByOtherTask)
	pending, err := s.ReadPendingEvents(t.Context(), 10)
	require.NoError(t, err)
	require.Empty(t, pending)
}

// A hub renumber of a relay-origin creation is never applied here: the author
// peer would keep the old number for the same uid. The creation is held, loudly.
func TestPushLoop_HubRenumberOfRelayOriginCreateIsHeld(t *testing.T) {
	ctx := t.Context()
	s, peer := movesTestStore(t), movesTestStore(t)
	require.NoError(t, peer.CreateNode(ctx, movesNode("MTIX-1", "", "relay parent")))
	ev, err := peer.ReadPendingEvents(ctx, 1)
	require.NoError(t, err)
	require.NoError(t, applyRelay(t, s, ev[0]))
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{ev[0].EventID: true}
	_, err = pushLoop(ctx, io.Discard, hub, s)
	require.NoError(t, err)
	require.Empty(t, hub.events)
	held, err := s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Equal(t, 1, held)
	for _, st := range []*sqlite.Store{s, peer} {
		n, err := st.GetNode(ctx, "MTIX-1")
		require.NoError(t, err)
		require.Equal(t, ev[0].EventID, n.UID)
	}
	_, err = pushLoop(ctx, io.Discard, hub, s) // a later push does not send or renumber it
	require.NoError(t, err)
	require.Empty(t, hub.events)
}

// A hub pull quarantined the event and the relay then applied it: the retry
// acknowledges the relay forward before it drops the hub envelope.
func TestRetryQuarantined_AcknowledgesMatchingRelayForward(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	e := hubCreateOf(t, "MTIX-1", clock.MustNewEventID(), "both paths")
	require.NoError(t, applyRelay(t, s, e))
	raw, err := json.Marshal(e)
	require.NoError(t, err)
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{
			EventID: e.EventID, Source: "pull", RawEvent: string(raw), Reason: "held",
			FirstSeen: "2026-07-01T00:00:00Z", LastAttempt: "2026-07-01T00:00:00Z", CLIVersion: "test",
		})
	}))
	_, err = retryQuarantinedEvents(ctx, testIngest(nil), s, 100)
	require.NoError(t, err)
	pending, err := s.ReadPendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Empty(t, pending, "the event came from the hub: it must not be pushed back")
}
