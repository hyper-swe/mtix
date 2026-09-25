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
)

// The page-progress guard of the keyset loops (MTIX-95.4 review): the
// cursor pass (pullLoop), the clone (cloneLoop) and the clone's check
// (preflightClone) each ask the hub for the page after a cursor and then
// move the cursor to the page's last event. A hub page that does not move
// past the cursor would make a loop ask for the same page forever. Each loop
// therefore stops with an error naming the cursor when a page ends at a
// lower Lamport clock than the cursor, or at an event it has already paged
// past at that clock. At one clock the hub orders event ids by its own
// collation, so a new event id there counts as progress.

// maxScriptedHubCalls bounds the pages one scriptedHub serves, so a loop
// that does not stop by itself fails the test instead of spinning.
const maxScriptedHubCalls = 20

// scriptedHub serves each cursor-pass page from serve, given the cursor it
// is asked for, and records every cursor in calls.
type scriptedHub struct {
	serve func(after transport.PullCursor) ([]*model.SyncEvent, bool)
	calls []transport.PullCursor
}

// PullEvents implements cursorPuller.
func (h *scriptedHub) PullEvents(_ context.Context, after transport.PullCursor, _ int) ([]*model.SyncEvent, bool, error) {
	h.calls = append(h.calls, after)
	if len(h.calls) > maxScriptedHubCalls {
		return nil, false, fmt.Errorf("scripted hub: more than %d page requests", maxScriptedHubCalls)
	}
	events, more := h.serve(after)
	return events, more, nil
}

// pageProgressCase is one hub behaviour the loops must stop on, or, when
// wantStopAt is nil, one they must follow to the end.
type pageProgressCase struct {
	name       string
	start      transport.PullCursor
	serve      func(after transport.PullCursor) ([]*model.SyncEvent, bool)
	wantCalls  int
	wantStopAt *transport.PullCursor // the cursor the error names; nil for no error
	applies    []*model.SyncEvent    // events a pull or clone applies when it does not stop
}

// pageProgressCases builds the hub behaviours, with fresh events for each
// test: the same page served again; a page that ends below the cursor's
// clock; two pages at one clock that alternate, each ending at a new event
// id until the first comes back; and, as progress, a page that ends at a
// new event id at the cursor's clock that sorts before the cursor's id byte
// by byte (another collation's order).
func pageProgressCases(t *testing.T) []pageProgressCase {
	t.Helper()
	same := remoteCreates(t, 5, 5)
	below := remoteCreates(t, 5, 5)
	aboveBelow := transport.PullCursor{Lamport: 9, EventID: below[1].EventID}
	ping := remoteCreates(t, 5, 5)
	pingAt := func(i int) transport.PullCursor { return transport.CursorAt(ping[i]) }
	// 'F' (0x46) sorts before 'f' (0x66) byte by byte; a case-insensitive
	// collation orders these two ids the other way round.
	lower := remoteCreateAt(t, "TEST-1", 5)
	lower.EventID = "0193FB00-0000-7000-8000-00000000000C"
	lowerCursor := transport.PullCursor{Lamport: 5, EventID: "0193fb00-0000-7000-8000-00000000000b"}
	return []pageProgressCase{
		{name: "the same page served again", serve: func(transport.PullCursor) ([]*model.SyncEvent, bool) {
			return same, true
		}, wantCalls: 2, wantStopAt: ptrCursor(transport.CursorAt(same[1]))},
		{name: "a page that ends below the cursor's clock", start: aboveBelow,
			serve:     func(transport.PullCursor) ([]*model.SyncEvent, bool) { return below, true },
			wantCalls: 1, wantStopAt: ptrCursor(aboveBelow)},
		{name: "two pages at one clock that alternate", serve: func(after transport.PullCursor) ([]*model.SyncEvent, bool) {
			if after == pingAt(0) {
				return ping[1:], true
			}
			return ping[:1], true
		}, wantCalls: 3, wantStopAt: ptrCursor(pingAt(1))},
		{name: "a new event id at the cursor's clock that sorts first byte by byte", start: lowerCursor,
			serve: func(transport.PullCursor) ([]*model.SyncEvent, bool) {
				return []*model.SyncEvent{lower}, false
			}, wantCalls: 1, applies: []*model.SyncEvent{lower}},
	}
}

// ptrCursor returns a pointer to c.
func ptrCursor(c transport.PullCursor) *transport.PullCursor { return &c }

// requirePageProgressOutcome asserts a loop's result for tt: the number of
// pages it asked for, and either an error that names the cursor it stopped
// at, or no error.
func requirePageProgressOutcome(t *testing.T, tt pageProgressCase, hub *scriptedHub, err error) {
	t.Helper()
	require.Len(t, hub.calls, tt.wantCalls, "pages asked for")
	if tt.wantStopAt == nil {
		require.NoError(t, err)
		return
	}
	require.ErrorContains(t, err, "does not advance past the cursor")
	require.ErrorContains(t, err, fmt.Sprintf("cursor: lamport %d, event %q", tt.wantStopAt.Lamport, tt.wantStopAt.EventID))
}

// TestPullLoop_HubPageNotAfterCursor_StopsNamingCursor: the cursor pass
// stops with an error naming its cursor, without applying the page, when a
// page does not advance past the cursor, and follows a page that does.
func TestPullLoop_HubPageNotAfterCursor_StopsNamingCursor(t *testing.T) {
	initTestApp(t)
	for _, tt := range pageProgressCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			hub := &scriptedHub{serve: tt.serve}

			_, _, err := pullLoop(context.Background(), testIngest(nil), hub, app.store, tt.start, 2)

			requirePageProgressOutcome(t, tt, hub, err)
			for _, e := range tt.applies {
				requireAppliedOnce(t, e)
			}
		})
	}
}

// TestCloneLoop_HubPageNotAfterCursor_StopsNamingCursor: the clone stops
// with an error naming its cursor when a page does not advance past it, and
// then saves no pull cursor.
func TestCloneLoop_HubPageNotAfterCursor_StopsNamingCursor(t *testing.T) {
	initTestApp(t)
	for _, tt := range pageProgressCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			hub := &scriptedHub{serve: tt.serve}

			_, _, err := cloneLoop(context.Background(), &bytes.Buffer{}, hub, app.store, tt.start, 2)

			requirePageProgressOutcome(t, tt, hub, err)
			if tt.wantStopAt != nil {
				requireSavedCursor(t, transport.PullCursor{})
				return
			}
			for _, e := range tt.applies {
				requireAppliedOnce(t, e)
			}
		})
	}
}

// TestPreflightClone_HubPageNotAfterCursor_StopsNamingCursor: the clone's
// check stops with an error naming its cursor when a page does not advance
// past it.
func TestPreflightClone_HubPageNotAfterCursor_StopsNamingCursor(t *testing.T) {
	initTestApp(t)
	for _, tt := range pageProgressCases(t) {
		t.Run(tt.name, func(t *testing.T) {
			hub := &scriptedHub{serve: tt.serve}

			err := preflightClone(context.Background(), testIngest(nil), hub, tt.start, 0, 2)

			requirePageProgressOutcome(t, tt, hub, err)
		})
	}
}

// TestPageCursorAdvance_HigherClock_KeepsOnlyThatClocksPageEnds: the event
// ids a loop remembers are those of page ends at the cursor's current
// Lamport clock only; a page that ends at a higher clock forgets the
// earlier ones, so the memory a long pull or clone holds stays bounded by
// the pages of one clock, not by the size of the log.
func TestPageCursorAdvance_HigherClock_KeepsOnlyThatClocksPageEnds(t *testing.T) {
	events := remoteCreates(t, 5, 5, 7, 7, 9)
	page := newPageCursor(transport.PullCursor{})
	for _, step := range []struct {
		page []*model.SyncEvent
		want []string
	}{
		{events[0:1], []string{events[0].EventID}},
		{events[1:2], []string{events[0].EventID, events[1].EventID}},
		{events[2:3], []string{events[2].EventID}},
		{events[3:4], []string{events[2].EventID, events[3].EventID}},
		{events[4:5], []string{events[4].EventID}},
	} {
		require.NoError(t, page.advance(step.page))
		got := make([]string, 0, len(page.passed))
		for id := range page.passed {
			got = append(got, id)
		}
		require.ElementsMatch(t, step.want, got)
		require.Equal(t, transport.CursorAt(step.page[len(step.page)-1]), page.at)
	}
}
