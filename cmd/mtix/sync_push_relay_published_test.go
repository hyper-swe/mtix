// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
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
	tot, err := pushLoop(ctx, io.Discard, hub, s)
	require.NoError(t, err)
	require.Zero(t, tot.renumbered, "a relay hold is not a renumber")
	require.Equal(t, 1, tot.relayHeld)
	require.Equal(t, "", relayHeldClause(0))
	require.Equal(t, ", 1 relay-carried held (never renumbered)", relayHeldClause(tot.relayHeld))
	require.Empty(t, hub.events)
	n, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, id, n.UID)
	ok, detail := checkHeldPushEvents(ctx, s)
	require.False(t, ok)
	for _, want := range []string{"permanent", "never renumbered", "relay-only peers",
		"only adds held events", "escalate"} {
		require.Contains(t, detail, want)
	}
	for _, banned := range []string{"mtix create", "mtix cancel", "resolved"} {
		require.NotContains(t, detail, banned)
	}
}

// A creation never published to the relay is renumbered locally, as before.
func TestPushLoop_HubRenumberOfUnpublishedOwnCreateIsRenumbered(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	id := ownCreate(t, s)
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{id: true}
	tot, err := pushLoop(ctx, io.Discard, hub, s)
	require.NoError(t, err)
	require.Equal(t, 1, tot.renumbered)
	require.Zero(t, tot.relayHeld)
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

// An earlier event is published and then this create is journaled after the
// cursor: only the earlier row is published (a rowid-at-or-after compare would
// call both published).
func TestIsRelayPublished_PublishEarlierThenCreate(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	first := ownCreate(t, s)
	publish(t, s, first)
	require.NoError(t, s.CreateNode(ctx, movesNode("MTIX-2", "", "later task")))
	ev, err := s.ReadPendingEvents(ctx, 10)
	require.NoError(t, err)
	require.Len(t, ev, 2)
	later := ev[1].EventID
	got, err := s.IsRelayPublished(ctx, later)
	require.NoError(t, err)
	require.False(t, got)
	got, err = s.IsRelayPublished(ctx, first)
	require.NoError(t, err)
	require.True(t, got)
}

// A rewound cursor (gap repair or an operator reset) never un-publishes: the
// relay still carries the task under its number.
func TestIsRelayPublished_SurvivesCursorRewind(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	id := ownCreate(t, s)
	publish(t, s, id)
	require.NoError(t, s.RepublishRelayFrom(ctx, 0))
	got, err := s.IsRelayPublished(ctx, id)
	require.NoError(t, err)
	require.True(t, got, "RepublishRelayFrom must not un-publish")
	require.NoError(t, s.ResetRelayPublisher(ctx, 0, 1))
	got, err = s.IsRelayPublished(ctx, id)
	require.NoError(t, err)
	require.True(t, got, "ResetRelayPublisher must not un-publish")
}

// A row the publisher marks for framing counts as published before the
// cursor advances, so a renumber cannot slip between the append and the
// advance.
func TestIsRelayPublished_ReadForFramingCounts(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	id := ownCreate(t, s)
	rows, err := s.ReadRelayJournalSince(ctx, 0, 10)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	got, err := s.IsRelayPublished(ctx, id)
	require.NoError(t, err)
	require.False(t, got, "a plain read is not publication")
	require.NoError(t, s.MarkRelayFraming(ctx, rows[0].Seq))
	got, err = s.IsRelayPublished(ctx, id)
	require.NoError(t, err)
	require.True(t, got)
}

// The hub renumber and the publisher can interleave: a publisher in another
// process frames the create between the published check and the renumber. The
// renumber re-checks inside its own transaction and holds instead.
func TestPushLoop_CreateFramedBetweenCheckAndRenumberIsHeld(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	id := ownCreate(t, s)
	gapCtx := context.WithValue(ctx, renumberGapKey{}, func() {
		rows, err := s.ReadRelayJournalSince(ctx, 0, 10)
		require.NoError(t, err)
		require.NoError(t, s.MarkRelayFraming(ctx, rows[len(rows)-1].Seq))
	})
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{id: true}
	tot, err := pushLoop(gapCtx, io.Discard, hub, s)
	require.NoError(t, err)
	require.Zero(t, tot.renumbered)
	require.Equal(t, 1, tot.relayHeld)
	n, err := s.GetNode(ctx, "MTIX-1")
	require.NoError(t, err)
	require.Equal(t, id, n.UID, "the task keeps its number")
}

// The publish mark only rises: a lower mark or a lower cursor advance after a
// rewind never lowers it.
func TestIsRelayPublished_MarkOnlyRises(t *testing.T) {
	ctx := t.Context()
	s := movesTestStore(t)
	first := ownCreate(t, s)
	require.NoError(t, s.CreateNode(ctx, movesNode("MTIX-2", "", "second")))
	ev, err := s.ReadPendingEvents(ctx, 10)
	require.NoError(t, err)
	second := ev[1].EventID
	seqs, err := s.LookupRelayJournalSeqs(ctx, []string{first, second})
	require.NoError(t, err)
	require.NoError(t, s.MarkRelayFraming(ctx, seqs[second]))
	require.NoError(t, s.RepublishRelayFrom(ctx, 0))
	require.NoError(t, s.MarkRelayFraming(ctx, seqs[first]))
	require.NoError(t, s.AdvanceRelayPushCursor(ctx, seqs[first], 2))
	got, err := s.IsRelayPublished(ctx, second)
	require.NoError(t, err)
	require.True(t, got, "a lower mark must not overwrite a higher one")
}
