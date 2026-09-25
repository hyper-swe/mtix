// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// The clone's second check, made while it applies (MTIX-95.11 round 4),
// driven through checkThenClone with fake cursor pullers.

// latePushHub serves pullEvents like fakeLateHub's cursor pass and, from
// PullEvents call number after+1 on, also the late events: a teammate pushes
// them after the clone's check has read the log.
type latePushHub struct {
	fakeLateHub
	after int
	late  []*model.SyncEvent
}

func (h *latePushHub) PullEvents(ctx context.Context, since int64, limit int) ([]*model.SyncEvent, bool, error) {
	if len(h.pullCalls) == h.after {
		h.pullEvents = append(h.pullEvents, h.late...)
	}
	return h.fakeLateHub.PullEvents(ctx, since, limit)
}

// TestCheckThenClone_EventPushedAfterCheck_StopsCloneKeepsClock: an event
// stamped far beyond the jump bound, pushed after the check passed, is
// refused by the apply's own check: the batches before it stay applied,
// the refusal says so, and the local clock never takes the extreme value.
func TestCheckThenClone_EventPushedAfterCheck_StopsCloneKeepsClock(t *testing.T) {
	initTestApp(t)
	events := offlineEvents(t)
	extreme := *events[2]
	extreme.LamportClock = validator.MaxLamportClock - 2
	hub := &latePushHub{fakeLateHub: fakeLateHub{pullEvents: events[:2]}, after: 2,
		late: []*model.SyncEvent{&extreme}}

	pulled, _, stage, err := checkThenClone(context.Background(), &bytes.Buffer{}, hub, 0, 1)

	require.ErrorIs(t, err, errCloneRefused)
	require.Equal(t, "clone loop", stage)
	require.ErrorContains(t, err, extreme.EventID)
	require.ErrorContains(t, err, "the batches before it were applied")
	require.Equal(t, 2, pulled, "the two batches before the refusal")
	require.Equal(t, int64(2), peerLamport(t), "the two checked events applied; the extreme clock never did")
	requireNotApplied(t, &extreme)
	require.Equal(t, []int64{0, 1, 0, 1, 2}, hub.pullCalls, "two check calls, then the clone's own pages")
}

// TestApplyBatch_BoundOneOverConsecutiveClocks_AllApplied: the apply checks
// each event against the local clock as it advances inside the batch, so a
// history of clocks 1, 2 and 3 passes a bound of 1.
func TestApplyBatch_BoundOneOverConsecutiveClocks_AllApplied(t *testing.T) {
	initTestApp(t)
	in := testIngest(nil)
	in.maxJump = 1

	require.NoError(t, applyBatch(context.Background(), in, app.store, offlineEvents(t)))

	require.Equal(t, int64(3), peerLamport(t))
}

// TestCheckThenClone_ResumeAboveBound_Passes: a resumed clone checks from
// the store's clock, not from zero: with the local clock at 2^40 (the
// batches an interrupted clone applied), events at 2^40+1 and 2^40+2 pass
// the default bound and apply.
func TestCheckThenClone_ResumeAboveBound_Passes(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	const base = int64(1) << 40
	_, err := app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.lamport'`, base)
	require.NoError(t, err)
	events := offlineEvents(t)[:2]
	for i, e := range events {
		e.LamportClock = base + int64(i) + 1
	}
	hub := &fakeLateHub{pullEvents: events}

	pulled, batches, _, err := checkThenClone(ctx, &bytes.Buffer{}, hub, base, 10)

	require.NoError(t, err)
	require.Equal(t, 2, pulled)
	require.Equal(t, 1, batches)
	require.Equal(t, base+2, peerLamport(t))
}

// TestCheckThenClone_Refused_LeavesQuarantineAndSweepUnchanged: a clone
// refused by its check writes nothing at all: an existing quarantine row
// and meta.sync.last_sweep_at stay as they were (the reset runs only after
// the check passes), and no event applies.
func TestCheckThenClone_Refused_LeavesQuarantineAndSweepUnchanged(t *testing.T) {
	initTestApp(t)
	quarantineN(t, 1)
	setLastSweep(t, "2026-09-24T09:00:00Z")
	events := offlineEvents(t)
	extreme := *events[2]
	extreme.LamportClock = validator.MaxLamportClock - 2
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{events[0], events[1], &extreme}}

	_, _, stage, err := checkThenClone(context.Background(), &bytes.Buffer{}, hub, 0, 10)

	require.ErrorIs(t, err, errCloneRefused)
	require.Equal(t, "clone check", stage)
	require.ErrorContains(t, err, "nothing was written")
	require.Len(t, quarantined(t), 1, "the quarantine row stays")
	require.Equal(t, "2026-09-24T09:00:00Z", lastSweep(t), "the sweep time stays")
	require.Zero(t, peerLamport(t))
	requireNotApplied(t, events[0])
}
