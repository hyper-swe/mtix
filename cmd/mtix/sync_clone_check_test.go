// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Clone runs the pull's checks (MTIX-95.11 round 3): `mtix sync clone`
// stays all-or-nothing, but before it writes anything it runs, on every
// hub event it will apply, the checks pull runs (the Lamport clock, then
// the FR-18.7 envelope, a hub row that did not decode included), and it
// refuses the whole clone when any event fails, naming the event and the
// reason and pointing to discard-local plus pull, which quarantines it.

// TestPreflightClone_EventPullWouldQuarantine_Refused: each kind of event
// pull would quarantine refuses the clone, with the event id, the reason
// (quoting the decode note of a malformed row) and the recovery steps; a
// clean history passes. The running clock is the highest clock checked so
// far, as the clone would have applied it.
func TestPreflightClone_EventPullWouldQuarantine_Refused(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(e *model.SyncEvent)
		maxJump int64
		want    string
	}{
		{"clock just below 2^53", func(e *model.SyncEvent) { e.LamportClock = validator.MaxLamportClock - 2 },
			validator.DefaultMaxLamportJump, "sync.max_lamport_jump"},
		{"clock at 2^53", func(e *model.SyncEvent) { e.LamportClock = validator.MaxLamportClock },
			validator.DefaultMaxLamportJump, "lamport_clock at or above 2^53"},
		{"jump beyond a small bound", func(e *model.SyncEvent) { e.LamportClock = 20 }, 10,
			"sync.max_lamport_jump"},
		{"hub row that did not decode", func(e *model.SyncEvent) {
			e.VectorClock, e.Malformed = nil, "vector_clock does not decode: json: cannot unmarshal string"
		}, validator.DefaultMaxLamportJump, "vector_clock does not decode: json: cannot unmarshal string"},
		{"bad author_id", func(e *model.SyncEvent) { e.AuthorID = "Not Valid" },
			validator.DefaultMaxLamportJump, "author_id"},
		{"oversized payload", func(e *model.SyncEvent) { e.Payload = oversizedPayload() },
			validator.DefaultMaxLamportJump, "payload too large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			events := offlineEvents(t)
			bad := *events[2]
			tt.mutate(&bad)
			hub := &fakeLateHub{pullEvents: []*model.SyncEvent{events[0], events[1], &bad}}
			in := testIngest(nil)
			in.maxJump = tt.maxJump

			err := preflightClone(context.Background(), in, hub, transport.PullCursor{}, 0, 1)

			require.ErrorIs(t, err, errCloneRefused)
			require.ErrorContains(t, err, bad.EventID)
			require.ErrorContains(t, err, tt.want)
			require.ErrorContains(t, err, "nothing was written")
			require.ErrorContains(t, err, cloneRecoveryText)
			require.Equal(t, []int64{0, 1, 2}, hub.pullCalls, "paged through the history, --batch-size at a time")
		})
	}
	t.Run("clean history passes, running clock from the checked events", func(t *testing.T) {
		initTestApp(t)
		events := offlineEvents(t)
		hub := &fakeLateHub{pullEvents: events}
		in := testIngest(nil)
		in.maxJump = 1

		require.NoError(t, preflightClone(context.Background(), in, hub, transport.PullCursor{}, 0, 2))
		require.Equal(t, []int64{0, 2}, hub.pullCalls)
	})
	t.Run("hub failure is returned", func(t *testing.T) {
		initTestApp(t)
		hub := &fakeLateHub{pullErr: context.DeadlineExceeded}
		err := preflightClone(context.Background(), testIngest(nil), hub, transport.PullCursor{}, 0, 10)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.NotErrorIs(t, err, errCloneRefused)
	})
}

// TestApplyBatch_CloneChecksEveryEvent: the clone's apply runs the same
// checks on each event, so an event pushed to the hub after the clone's
// check stops the clone; its batch rolls back and nothing of it is kept.
func TestApplyBatch_CloneChecksEveryEvent(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	bad := *events[1]
	bad.LamportClock = validator.MaxLamportClock - 2

	err := applyBatch(context.Background(), testIngest(nil), app.store, []*model.SyncEvent{events[0], &bad})

	require.ErrorIs(t, err, errCloneRefused)
	require.ErrorContains(t, err, bad.EventID)
	require.ErrorContains(t, err, "the batches before it were applied")
	requireNotApplied(t, events[0])
	require.Zero(t, peerLamport(t))
}

// TestPreflightClone_FutureStamped_WarnsOnly: as in pull, an event stamped
// more than 24h ahead of this machine's clock only warns.
func TestPreflightClone_FutureStamped_WarnsOnly(t *testing.T) {
	initTestApp(t)
	ahead := *offlineEvents(t)[0]
	ahead.WallClockTS = time.Now().Add(48 * time.Hour).UnixMilli()
	var stderr bytes.Buffer

	err := preflightClone(context.Background(), testIngest(&stderr), &fakeLateHub{pullEvents: []*model.SyncEvent{&ahead}}, transport.PullCursor{}, 0, 10)

	require.NoError(t, err)
	require.Contains(t, stderr.String(), "is stamped more than 24h ahead of this machine's clock")
}

// TestPull_EventAlreadyApplied_NotQuarantined: an event this store already
// holds (here applied without the checks, as an older clone did) is not
// run through the checks again: IdempotentApply dedupes it, so the first
// pull does not quarantine or report it.
func TestPull_EventAlreadyApplied_NotQuarantined(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	big := *offlineEvents(t)[0]
	big.Payload = oversizedPayload()
	mustIdempotentApply(t, &big)
	var stderr bytes.Buffer
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{&big}, hubNows: []time.Time{sweepHubT1}}

	got, err := pullThenSweep(ctx, testIngest(&stderr), hub, app.store, transport.PullCursor{}, 100)

	require.NoError(t, err)
	require.Equal(t, 1, got.pulled, "counted as applied: the store holds it")
	require.Empty(t, quarantined(t))
	require.NotContains(t, stderr.String(), "quarantined event")
}
