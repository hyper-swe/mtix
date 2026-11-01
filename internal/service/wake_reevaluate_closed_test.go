// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// heldWithOwnAbove leaves the fixture agent held on a foreign event with an own event above it,
// the guard lifted, ready for re-evaluation.
func heldWithOwnAbove(t *testing.T) (*scanFixture, int64, int64) {
	t.Helper()
	f := newScanFixture(t)
	held := f.addEvent("sender")
	own := f.addEvent(f.key.AgentID)
	f.wstore.hops[held] = 3
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	_, hold := f.mark()
	require.True(t, hold)
	f.wstore.hops[held] = 0
	f.gate = func(context.Context, model.WakeKey) (bool, error) { return true, nil }
	return f, held, own
}

func requireUntouched(t *testing.T, f *scanFixture) {
	t.Helper()
	require.Empty(t, f.adapter.seqs(), "nothing may be sent when authors cannot be judged")
	require.Zero(t, f.ledgerRows())
	hw, hold := f.mark()
	require.Zero(t, hw)
	require.True(t, hold, "the hold stays")
}

func TestReevaluate_StoreCannotReadAuthors_FailsClosed(t *testing.T) {
	f, _, _ := heldWithOwnAbove(t)
	d := f.dispatcherOver(noInboxStore{f.wstore})
	err := d.Reevaluate(t.Context(), f.key.AgentID)
	require.ErrorIs(t, err, model.ErrInvalidInput)
	requireUntouched(t, f)
}

func TestReevaluate_AuthorReadFails_FailsClosed(t *testing.T) {
	f, _, _ := heldWithOwnAbove(t)
	boom := errors.New("inbox unavailable")
	d := f.dispatcherOver(inboxOverrideStore{f.wstore, func() ([]sqlite.InboxEvent, error) { return nil, boom }})
	err := d.Reevaluate(t.Context(), f.key.AgentID)
	require.ErrorIs(t, err, boom)
	requireUntouched(t, f)
}

// An event whose author cannot be determined (acknowledged since the candidate list) is not
// sent, and the pass is cut there: the mark never moves past an unsent event.
func TestReevaluate_AuthorMissingFromRead_BlocksPassAtThatEvent(t *testing.T) {
	f, held, _ := heldWithOwnAbove(t)
	d := f.dispatcherOver(inboxOverrideStore{f.wstore, func() ([]sqlite.InboxEvent, error) { return nil, nil }})
	require.NoError(t, d.Reevaluate(t.Context(), f.key.AgentID))
	require.Empty(t, f.adapter.seqs(), "event %d has an unknown author and must not be woken", held)
	require.Zero(t, f.ledgerRows())
	hw, hold := f.mark()
	require.Zero(t, hw, "the mark must not move past an event that was neither sent nor judged")
	require.True(t, hold)

	// The next pass sees the inbox as it now stands and handles the events in order.
	require.NoError(t, f.d.Reevaluate(t.Context(), f.key.AgentID))
	require.Equal(t, []int64{held}, f.adapter.seqs())
}

// Re-evaluation on a context carrying the registered session must not refresh last_seen.
func TestReevaluate_SessionContext_DoesNotRefreshLastSeen(t *testing.T) {
	f, _, _ := heldWithOwnAbove(t)
	_, err := f.store.WriteDB().ExecContext(t.Context(), "UPDATE agent_registrations SET session_handle='h'")
	require.NoError(t, err)
	ctx := sqlite.WithSessionTouch(t.Context(), sqlite.SessionTouch{HostID: f.key.HostID, UID: f.key.UID, Handle: "h"})
	require.NoError(t, f.d.Reevaluate(ctx, f.key.AgentID))
	var seen string
	require.NoError(t, f.store.ReadDB().QueryRow("SELECT last_seen FROM agent_registrations").Scan(&seen))
	require.Equal(t, "2026-01-01T00:00:00Z", seen)
}
