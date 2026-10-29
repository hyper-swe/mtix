// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Re-evaluation must meet the scan's rule: an agent is never woken for an event it wrote.
func TestWakeScan_ReevaluateWithOwnEventAboveHeld_NeverWakesOwnEvent(t *testing.T) {
	f := newScanFixture(t)
	held := f.addEvent("sender")
	own := f.addEvent(f.key.AgentID)
	f.gate = f.scanner(always).Gate
	f.wstore.hops[held] = 3
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	_, hold := f.mark()
	require.True(t, hold)

	f.wstore.hops[held] = 0
	require.NoError(t, f.d.Reevaluate(t.Context(), f.key.AgentID))

	require.Equal(t, []int64{held}, f.adapter.seqs(), "the own event must not be sent")
	hw, hold := f.mark()
	require.Equal(t, own, hw, "the own event is covered once the held event is handled")
	require.False(t, hold)
}

// A claim failure on a foreign event sets no hold. An own event above it must not let the
// mark move past it (the cover obeys the same ordering as a send).
func TestWakeScan_OwnEventAboveBlockedForeign_MarkStays(t *testing.T) {
	f := newScanFixture(t)
	foreign := f.addEvent("sender")
	own := f.addEvent(f.key.AgentID)
	f.wstore.failSeq, f.wstore.failOnce = foreign, true

	require.ErrorIs(t, f.scanner(always).Scan(t.Context()), model.ErrConflict)
	hw, _ := f.mark()
	require.Zero(t, hw, "the own event must not carry the mark past the blocked foreign event")
	require.Empty(t, f.adapter.seqs())

	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{foreign}, f.adapter.seqs())
	hw, _ = f.mark()
	require.Equal(t, own, hw)
}

// An unreadable mark while covering an own event blocks the registration for the pass.
func TestWakeScan_OwnEventEligibilityReadError_BlocksLaterForeignEvent(t *testing.T) {
	f := newScanFixture(t)
	f.addEvent(f.key.AgentID)
	foreign := f.addEvent("sender")
	f.wstore.failMark = true

	require.ErrorIs(t, f.scanner(always).Scan(t.Context()), model.ErrConflict)
	require.Empty(t, f.adapter.seqs(), "nothing may be sent after an unjudged event")

	require.NoError(t, f.scanner(always).Scan(t.Context()))
	require.Equal(t, []int64{foreign}, f.adapter.seqs())
}

// A scan on a context that carries the registered session must not refresh last_seen:
// otherwise the scan would keep a dead row alive.
func TestWakeScan_ScanWithSessionContext_DoesNotRefreshLastSeen(t *testing.T) {
	f := newScanFixture(t)
	f.addEvent("sender")
	_, err := f.store.WriteDB().ExecContext(t.Context(), "UPDATE agent_registrations SET session_handle='h'")
	require.NoError(t, err)
	f.liveness.state.Store(int32(service.LiveDead))
	ctx := sqlite.WithSessionTouch(t.Context(), sqlite.SessionTouch{HostID: f.key.HostID, UID: f.key.UID, Handle: "h"})

	require.NoError(t, f.scanner(always).Scan(ctx))

	var seen string
	require.NoError(t, f.store.ReadDB().QueryRow("SELECT last_seen FROM agent_registrations").Scan(&seen))
	require.Equal(t, "2026-01-01T00:00:00Z", seen)
}

func TestWakeScan_GateWithoutRouteOrRegistration_Rejects(t *testing.T) {
	f := newScanFixture(t)
	s := f.scanner(always)
	f.adapter.live.Store(false)
	ok, err := s.Gate(t.Context(), f.key)
	require.NoError(t, err)
	require.False(t, ok, "no live route")
	f.adapter.live.Store(true)

	other := f.key
	other.AgentID = "someone-else"
	ok, err = s.Gate(t.Context(), other)
	require.NoError(t, err)
	require.False(t, ok, "no registration for the agent")

	old := f.key
	old.Epoch = f.key.Epoch + 1
	ok, err = s.Gate(t.Context(), old)
	require.NoError(t, err)
	require.False(t, ok, "registration of another epoch")
}

func TestWakeScan_StaleMarkWriteFails_ErrorSurfaces(t *testing.T) {
	f := newScanFixture(t)
	f.addEvent("sender")
	f.liveness.state.Store(int32(service.LiveDead))
	opts := f.options(always)
	opts.Store = staleFailStore{f.store}
	s, err := service.NewWakeScanner(opts)
	require.NoError(t, err)
	require.ErrorIs(t, s.Scan(t.Context()), model.ErrConflict)
	require.Empty(t, f.adapter.seqs())
}

// Only the recipient's own events: no gate applies, the mark moves over them.
func TestWakeScan_OnlyOwnEvents_GatesDoNotApply(t *testing.T) {
	f := newScanFixture(t)
	own := f.addEvent(f.key.AgentID)
	f.wakeable.Store(false)
	f.adapter.live.Store(false)
	require.NoError(t, f.scanner(always).Scan(t.Context()))
	hw, _ := f.mark()
	require.Equal(t, own, hw)
}

func TestHooksDispatch_WakePassError_IsLogged(t *testing.T) {
	f := newScanFixture(t)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	scan := &countingScan{err: errors.New("scan exploded")}
	service.NewHooksDispatcher(f.store, t.TempDir(), logger).WithWakeScanner(scan).Dispatch(context.Background())
	require.Contains(t, buf.String(), "wake scan failed")
	require.Contains(t, buf.String(), "scan exploded")
}
