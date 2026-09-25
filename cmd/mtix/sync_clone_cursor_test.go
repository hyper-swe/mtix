// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// The clone's cursors (MTIX-95.4; ADR-006 D6, D16): `mtix sync clone` pages
// by the same (lamport_clock, event_id) keyset as pull, keeps its --resume
// checkpoint as a tuple, and on completion saves the pull cursor, both
// halves, at the last event it cloned, so the first pull after a clone
// fetches only the events pushed since. The fake hub is fakeLateHub
// (sync_pull_sweep_test.go); the real-Postgres paths are in
// sync_pull_cursor_pg_test.go.

// requireCloneCheckpoint asserts the peer's saved clone checkpoint.
func requireCloneCheckpoint(t *testing.T, want transport.PullCursor) {
	t.Helper()
	got, err := readCloneCheckpoint(context.Background(), app.store, true)
	require.NoError(t, err)
	require.Equal(t, want, got, "saved clone checkpoint")
}

// TestClone_SetsPullCursor: a clone with --batch-size 2 over a log where
// three events share a Lamport clock clones every event, saves its
// checkpoint and the pull cursor at the last event, and the first pull after
// it asks the hub only for the events after that event and applies only the
// one pushed since. Before MTIX-95.4 the clone skipped the tied event past
// the page boundary and never wrote the pull cursor, so the first pull read
// the whole hub log again.
func TestClone_SetsPullCursor(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := remoteCreates(t, 5, 5, 5, 6)
	hub := &fakeLateHub{pullEvents: events}

	pulled, batches, stage, err := checkThenClone(ctx, &bytes.Buffer{}, hub, transport.PullCursor{}, 2)

	require.NoError(t, err, stage)
	require.Equal(t, 4, pulled)
	require.Equal(t, 2, batches)
	for _, e := range events {
		requireAppliedOnce(t, e)
	}
	last := transport.CursorAt(events[3])
	requireCloneCheckpoint(t, last)
	requireSavedCursor(t, last)

	newer := remoteCreateAt(t, "TEST-5", 7)
	hub.pullEvents = append(hub.pullEvents, newer)
	hub.pullCursors = nil
	cursor, err := readLastPulledClock(ctx, app.store)
	require.NoError(t, err)
	applied, batches, err := pullLoop(ctx, testIngest(nil), hub, app.store, cursor, 100)

	require.NoError(t, err)
	require.Equal(t, 1, applied, "the first pull after the clone applies only the newer event")
	require.Equal(t, 1, batches)
	require.Equal(t, []transport.PullCursor{last}, hub.pullCursors, "it starts after the clone's last event")
	requireAppliedOnce(t, newer)
	requireSavedCursor(t, transport.CursorAt(newer))
}

// TestCloneLoop_NothingToClone_SavesCursorsAtStart: a clone that finds no
// event after its start position applies nothing and still ends with the
// pull cursor at that position: the start of the log for an empty hub, the
// checkpoint for a resume with nothing left to clone.
func TestCloneLoop_NothingToClone_SavesCursorsAtStart(t *testing.T) {
	resumed := transport.PullCursor{Lamport: 5, EventID: "0193fb00-0000-7000-8000-000000000005"}
	tests := []struct {
		name  string
		start transport.PullCursor
	}{
		{"empty hub", transport.PullCursor{}},
		{"resume with nothing left", resumed},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			hub := &fakeLateHub{}

			pulled, batches, err := cloneLoop(context.Background(), &bytes.Buffer{}, hub, app.store, tt.start, 2)

			require.NoError(t, err)
			require.Zero(t, pulled)
			require.Zero(t, batches)
			require.Equal(t, []transport.PullCursor{tt.start}, hub.pullCursors)
			requireSavedCursor(t, tt.start)
		})
	}
}

// TestCloneLoop_ResumeFromCheckpoint_ContinuesAfterLastClonedEvent: an
// interrupted clone saved its checkpoint after two of three events that
// share a Lamport clock. --resume continues after the saved event: the
// third event at that clock is cloned and the two already applied are not
// fetched again. A checkpoint saved by a CLI before MTIX-95.4 has no event
// id; the resume then reads every event at the saved clock again and
// applies the ones it holds idempotently. Either way the clone ends with
// the pull cursor at the last event.
func TestCloneLoop_ResumeFromCheckpoint_ContinuesAfterLastClonedEvent(t *testing.T) {
	tests := []struct {
		name        string
		eventID     func(events []*model.SyncEvent) string
		wantPulled  int
		wantResumed func(events []*model.SyncEvent) transport.PullCursor
	}{
		{"tuple checkpoint", func(events []*model.SyncEvent) string { return events[1].EventID }, 2,
			func(events []*model.SyncEvent) transport.PullCursor { return transport.CursorAt(events[1]) }},
		{"Lamport-only checkpoint from before the upgrade", func([]*model.SyncEvent) string { return "" }, 4,
			func([]*model.SyncEvent) transport.PullCursor { return transport.PullCursor{Lamport: 5} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			events := remoteCreates(t, 5, 5, 5, 6)
			require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, events[:2]))
			setPeerMeta(t, "meta.sync.clone.checkpoint", "5")
			setPeerMeta(t, "meta.sync.clone.checkpoint_event_id", tt.eventID(events))
			since, err := readCloneCheckpoint(ctx, app.store, true)
			require.NoError(t, err)
			require.Equal(t, tt.wantResumed(events), since)
			hub := &fakeLateHub{pullEvents: events}

			pulled, _, err := cloneLoop(ctx, &bytes.Buffer{}, hub, app.store, since, 2)

			require.NoError(t, err)
			require.Equal(t, tt.wantPulled, pulled)
			require.Equal(t, since, hub.pullCursors[0])
			for _, e := range events {
				requireAppliedOnce(t, e)
			}
			requireCloneCheckpoint(t, transport.CursorAt(events[3]))
			requireSavedCursor(t, transport.CursorAt(events[3]))
		})
	}
}
