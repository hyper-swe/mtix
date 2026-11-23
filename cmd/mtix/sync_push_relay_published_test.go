// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"io"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// ownCreate makes a creation of this replica and returns its event id and uid.
func ownCreate(t *testing.T, s *sqlite.Store) string {
	t.Helper()
	require.NoError(t, s.CreateNode(t.Context(), movesNode("MTIX-1", "", "gateway task")))
	ev, err := s.ReadPendingEvents(t.Context(), 1)
	require.NoError(t, err)
	return ev[0].EventID
}

// publish moves the relay publish cursor past the event, as a relay tick does.
func publish(t *testing.T, s *sqlite.Store, eventID string) {
	t.Helper()
	seqs, err := s.LookupRelayJournalSeqs(t.Context(), []string{eventID})
	require.NoError(t, err)
	require.NoError(t, s.AdvanceRelayPushCursor(t.Context(), seqs[eventID], 2))
}

// A gateway's own creation that the relay already carries is never renumbered
// by a hub renumber: relay peers hold the old number for the uid. It is held
// and doctor fails (relay-carried tasks are never renumbered).
func TestPushLoop_HubRenumberOfPublishedOwnCreateIsHeld(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	id := ownCreate(t, s)
	publish(t, s, id)
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{id: true}
	_, err := pushLoop(ctx, io.Discard, hub, s)
	require.NoError(t, err)
	require.Empty(t, hub.events)
	n, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, id, n.UID)
	ok, detail := checkHeldPushEvents(ctx, s)
	require.False(t, ok)
	for _, want := range []string{"permanent", "never renumbered", "relay-only peers",
		`mtix create "<same title>" --under <same parent>`, `mtix cancel MTIX-1 --reason "duplicate of <new id>"`} {
		require.Contains(t, detail, want)
	}
	require.NotContains(t, detail, "escalate")
}

// A creation never published to the relay is renumbered locally, as before.
func TestPushLoop_HubRenumberOfUnpublishedOwnCreateIsRenumbered(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	id := ownCreate(t, s)
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{id: true}
	_, err := pushLoop(ctx, io.Discard, hub, s)
	require.NoError(t, err)
	require.True(t, hub.accepted[id])
	require.NotEqual(t, "MTIX-1", hub.events[len(hub.events)-1].NodeID)
	held, err := s.CountHeldPushEvents(ctx)
	require.NoError(t, err)
	require.Zero(t, held)
}

// The published predicate is the publish cursor: a row at or before it is
// published, one after it (or with no cursor row) is not.
func TestIsRelayPublished_FollowsPublishCursor(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	id := ownCreate(t, s)
	got, err := s.IsRelayPublished(ctx, id)
	require.NoError(t, err)
	require.False(t, got)
	publish(t, s, id)
	got, err = s.IsRelayPublished(ctx, id)
	require.NoError(t, err)
	require.True(t, got)
	got, err = s.IsRelayPublished(ctx, "no-such-event")
	require.NoError(t, err)
	require.False(t, got)
}
