// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// The pull cursor and the Lamport clock of quarantined events (MTIX-95.11
// round 2): the cursor decision comes from an event's clock, never from the
// reason recorded for it, so a row that is both extreme and malformed
// cannot move the cursor to its clock and stop the cursor pass.

// TestPull_ExtremeAndMalformed_CursorStays: a hub row whose Lamport clock is
// extreme (at or above 2^53, or beyond sync.max_lamport_jump) and that also
// fails another envelope rule is refused for its clock (the clock is
// checked first), the saved cursor and the local clock stay put, and a
// later valid event still arrives through the cursor pass.
func TestPull_ExtremeAndMalformed_CursorStays(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(e *model.SyncEvent)
		wantReason string
	}{
		{"bad author_id at 2^40", func(e *model.SyncEvent) {
			e.AuthorID, e.LamportClock = "BAD AUTHOR", 1<<40
		}, "sync.max_lamport_jump"},
		{"oversized payload just below 2^53", func(e *model.SyncEvent) {
			e.Payload, e.LamportClock = oversizedPayload(), validator.MaxLamportClock-5
		}, "sync.max_lamport_jump"},
		{"bad author_id above 2^53", func(e *model.SyncEvent) {
			e.AuthorID, e.LamportClock = "BAD AUTHOR", validator.MaxLamportClock+5
		}, "lamport_clock at or above 2^53"},
		{"bad project prefix and deep payload at 2^53", func(e *model.SyncEvent) {
			e.ProjectPrefix = "bad"
			e.Payload = []byte(`{"x":` + strings.Repeat("[", 12) + strings.Repeat("]", 12) + `}`)
			e.LamportClock = validator.MaxLamportClock
		}, "lamport_clock at or above 2^53"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			bad := *offlineEvents(t)[0]
			tt.mutate(&bad)
			hub := &fakeLateHub{events: []*model.SyncEvent{&bad}, pullEvents: []*model.SyncEvent{&bad},
				hubNows: []time.Time{sweepHubT1}}

			_, err := pullThenSweep(context.Background(), testIngest(nil), hub, app.store, transport.PullCursor{}, 100)

			require.NoError(t, err)
			requireQuarantinedAs(t, &bad, "pull", tt.wantReason)
			require.Zero(t, peerLamport(t))
			requireLaterEventViaCursorPass(t, hub)
		})
	}
}

// TestAdvancePullCursor_DecidesFromClock: the saved cursor moves to the
// last event of the batch whose clock is below 2^53 and at most maxJump
// above the local clock after the batch, its clock and its event id
// (MTIX-95.4); with no such event it does not move.
func TestAdvancePullCursor_DecidesFromClock(t *testing.T) {
	ev := func(clocks ...int64) []*model.SyncEvent {
		out := make([]*model.SyncEvent, 0, len(clocks))
		for i, c := range clocks {
			out = append(out, &model.SyncEvent{LamportClock: c, EventID: fmt.Sprintf("e%d", i)})
		}
		return out
	}
	tests := []struct {
		name           string
		events         []*model.SyncEvent
		local, maxJump int64
		wantIdx        int // index of the event the cursor moves to; -1 for none
	}{
		{"all within the bound", ev(1, 2, 3), 3, 10, 2},
		{"one past the jump bound", ev(1, 3, 14), 3, 10, 1},
		{"exactly the bound above", ev(1, 3, 13), 3, 10, 2},
		{"a Lamport tie: the last event at the clock", ev(3, 3, 3), 3, 10, 2},
		{"at 2^53 whatever the bound", ev(2, validator.MaxLamportClock), validator.MaxLamportClock - 1,
			1 << 62, 0},
		{"only an extreme event: the saved cursor stays", ev(1 << 40), 7, validator.DefaultMaxLamportJump, -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			saved, moved := advancePullCursor(tt.events, tt.local, tt.maxJump)
			if tt.wantIdx < 0 {
				require.False(t, moved)
				require.Equal(t, transport.PullCursor{}, saved)
				return
			}
			require.True(t, moved)
			require.Equal(t, transport.CursorAt(tt.events[tt.wantIdx]), saved)
		})
	}
}

// TestApplyPullBatch_LocalClockUnreadable_AbortsBatch: a local fault
// reading meta.sync.lamport is a transaction error: the batch rolls back
// and no hub event is quarantined because of it.
func TestApplyPullBatch_LocalClockUnreadable_AbortsBatch(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	_, err := app.store.WriteDB().ExecContext(context.Background(),
		`UPDATE meta SET value = 'x' WHERE key = 'meta.sync.lamport'`)
	require.NoError(t, err)

	held, err := applyPullBatch(context.Background(), testIngest(nil), app.store, quarantineSourcePull, events)

	require.ErrorContains(t, err, "meta.sync.lamport")
	require.Nil(t, held)
	require.Empty(t, quarantined(t))
	requireNotApplied(t, events[0])
}

// TestPull_AlreadyQuarantined_CursorPassLeavesItToRetry: an event the cursor
// pass meets again while it is quarantined is left to the quarantine retry:
// its row is not rewritten, no attempt is counted and it is not reported
// again; it still does not apply and does not move the cursor past a
// refused clock.
func TestPull_AlreadyQuarantined_CursorPassLeavesItToRetry(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	bad := *offlineEvents(t)[0]
	bad.AuthorID = "Not Valid"
	firstAt := time.Date(2026, 9, 25, 8, 0, 0, 0, time.UTC)
	in := testIngest(nil)
	in.now = func() time.Time { return firstAt }
	held, err := applyPullBatch(ctx, in, app.store, quarantineSourcePull, []*model.SyncEvent{&bad})
	require.NoError(t, err)
	require.Len(t, held, 1)
	var stderr strings.Builder
	in = testIngest(&stderr)
	in.now = func() time.Time { return firstAt.Add(time.Hour) }

	held, err = applyPullBatch(ctx, in, app.store, quarantineSourcePull, []*model.SyncEvent{&bad})

	require.NoError(t, err)
	require.Len(t, held, 1, "still held")
	q := quarantined(t)[bad.EventID]
	require.Equal(t, 1, q.Attempts, "no attempt counted by the cursor pass")
	require.Equal(t, firstAt.Format(time.RFC3339Nano), q.LastAttempt, "the row is not rewritten")
	require.Empty(t, stderr.String(), "not reported again")
	requireNotApplied(t, &bad)
}

// TestApplyPullBatch_AlreadyQuarantinedSkip_StillRunsAfterEach: the sweep's
// unstage callback runs for an event left to the quarantine as for any
// other, so a staged id never outlives it.
func TestApplyPullBatch_AlreadyQuarantinedSkip_StillRunsAfterEach(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	bad := *offlineEvents(t)[0]
	bad.AuthorID = "Not Valid"
	_, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull, []*model.SyncEvent{&bad})
	require.NoError(t, err)
	calls := 0
	after := func(_ *sql.Tx, e *model.SyncEvent) error {
		require.Equal(t, bad.EventID, e.EventID)
		calls++
		return nil
	}

	_, err = applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourceSweep, []*model.SyncEvent{&bad}, after)

	require.NoError(t, err)
	require.Equal(t, 1, calls)
}
