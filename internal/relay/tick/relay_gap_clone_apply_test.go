// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package tick_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The continuity check decides whether a clone STALLS; it never decides which
// records a clone APPLIES. A clone replays every retained record, as it did
// before the check existed, so last-writer-wins judges them against the other
// streams' events. Skipping or re-reading records by a stamp would revert a
// newer value silently, with no signal (MTIX-265).

// setTitle changes a node's title through the store, which journals the event.
func setTitle(t *testing.T, st *sqlite.Store, id, title string) {
	t.Helper()
	require.NoError(t, st.UpdateNode(context.Background(), id, &store.NodeUpdate{Title: &title}))
}

func titleIn(t *testing.T, st *sqlite.Store, id string) string {
	t.Helper()
	n, err := st.GetNode(context.Background(), id)
	require.NoError(t, err)
	return n.Title
}

// requireLaterPollsIdle polls the clone twice more: the first poll moved its
// cursor past the seeded segment 0, so later polls read nothing, apply nothing
// and stall nothing. The clone was seeded by the production seeding path.
func (g *gapRig) requireLaterPollsIdle(t *testing.T, stack *tick.Stack, peer string) {
	t.Helper()
	ctx := context.Background()
	moved, err := g.cloned.RelayIngestCursor(ctx, peer)
	require.NoError(t, err)
	require.Greater(t, moved.SegmentNo, uint64(0), "the first poll moved the cursor off the seed")
	for poll := 2; poll <= 3; poll++ {
		pass, passErr := stack.Pass(ctx)
		require.NoError(t, passErr)
		require.Empty(t, pass.Ingest.Stalls, "poll %d", poll)
		require.Zero(t, pass.Ingest.Applied, "poll %d", poll)
		now, curErr := g.cloned.RelayIngestCursor(ctx, peer)
		require.NoError(t, curErr)
		require.Equal(t, moved, now, "poll %d", poll)
	}
}

// TestRelayGap_CloneConvergesAfterAnOlderEpochReplay: A sets a title, resets
// its publisher epoch past that event, and B overwrites the title. A clone
// from B reads A's older-epoch segment again and must still end on B's value.
func TestRelayGap_CloneConvergesAfterAnOlderEpochReplay(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	g.a.journal(t, 1)
	_, err := g.stackA.Pass(ctx)
	require.NoError(t, err)
	setTitle(t, g.a.store, "PROJ-1", "fromA")
	_, err = g.stackA.Pass(ctx)
	require.NoError(t, err)
	_, err = g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.Equal(t, "fromA", titleIn(t, g.bStore, "PROJ-1"))

	tail, err := g.a.store.JournalTail(ctx)
	require.NoError(t, err)
	require.NoError(t, g.stackA.Publisher.ResetPeer(ctx, tail, 1))
	g.a.journal(t, 1)
	_, err = g.stackA.Pass(ctx)
	require.NoError(t, err)
	_, err = g.stackB.Pass(ctx)
	require.NoError(t, err)

	setTitle(t, g.bStore, "PROJ-1", "fromB")
	_, err = g.stackB.Pass(ctx)
	require.NoError(t, err)

	g.exportSnapshot(t, g.bStore, peerB)
	stack, pass := g.cloneFresh(t)
	require.Empty(t, pass.Ingest.Stalls)
	g.requireLaterPollsIdle(t, stack, peerA)
	require.Equal(t, "fromB", titleIn(t, g.cloned, "PROJ-1"),
		"the clone ends on the fleet's value, not the older epoch's")
}

// TestRelayGap_CloneKeepsDoneAndTheNewerTitleWithAnOpenQuarantineRow: the
// exporter holds an open quarantine row for a record of A's stream that A's
// later events were superseded. The clone must not re-read below the cursor
// and revert the task from done to in_progress.
func TestRelayGap_CloneKeepsDoneAndTheNewerTitleWithAnOpenQuarantineRow(t *testing.T) {
	ctx := context.Background()
	g := newGapRig(t)
	for i := 0; i < 2; i++ {
		g.a.journal(t, 1)
		_, err := g.stackA.Pass(ctx)
		require.NoError(t, err)
	}
	require.NoError(t, g.a.store.TransitionStatus(ctx, "PROJ-1", model.StatusInProgress, "", "a"))
	setTitle(t, g.a.store, "PROJ-1", "fromA")
	_, err := g.stackA.Pass(ctx)
	require.NoError(t, err)
	_, err = g.stackB.Pass(ctx)
	require.NoError(t, err)

	require.NoError(t, g.bStore.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "", "b"))
	setTitle(t, g.bStore, "PROJ-1", "fromB")
	_, err = g.stackB.Pass(ctx)
	require.NoError(t, err)
	require.NoError(t, g.bStore.RecordRelayQuarantine(ctx, sqlite.RelayQuarantineRecord{
		RelayQuarantineKey: sqlite.RelayQuarantineKey{PeerID: peerA, PubEpoch: 1, RS: 2},
		SegmentNo:          2, Reason: "refused by the gate", Payload: []byte("{}"), SeenAt: "2026-01-01T00:00:00Z",
	}))

	g.exportSnapshot(t, g.bStore, peerB)
	stack, pass := g.cloneFresh(t)
	require.Empty(t, pass.Ingest.Stalls)
	g.requireLaterPollsIdle(t, stack, peerA)
	got, err := g.cloned.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, model.StatusDone, got.Status, "a done task stays done on the clone")
	require.Equal(t, "fromB", got.Title)
}
