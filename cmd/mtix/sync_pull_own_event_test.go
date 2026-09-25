// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// A pulled hub copy of this replica's own event (MTIX-95.11 round 4). The
// hub row of an event this replica pushed can be changed on the hub, and
// until a pull acknowledges it the event is only in the local sync_events,
// not in applied_events. Such a copy is checked like any other event; only
// an event already in applied_events skips the checks.

// peerVectorClock reads the peer store's local vector clock.
func peerVectorClock(t *testing.T) string {
	t.Helper()
	var v string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT value FROM meta WHERE key = 'meta.sync.vector_clock'`).Scan(&v))
	return v
}

// TestPull_OwnEventHubCopyTampered_QuarantinedClockUnaffected: a hub copy of
// an own, not yet acknowledged event whose Lamport clock was raised to
// 2^53-1, or whose vector clock was given 151 entries, is quarantined; the
// local Lamport and vector clocks do not change, and a later local write
// still works and passes the hub's push validation.
func TestPull_OwnEventHubCopyTampered_QuarantinedClockUnaffected(t *testing.T) {
	tests := []struct {
		name       string
		tamper     func(e *model.SyncEvent)
		wantReason string
	}{
		{"lamport raised to 2^53-1", func(e *model.SyncEvent) { e.LamportClock = validator.MaxLamportClock - 1 },
			"sync.max_lamport_jump"},
		{"vector clock with 151 entries", func(e *model.SyncEvent) {
			vc := model.VectorClock{}
			for i := 0; i < 151; i++ {
				vc[fmt.Sprintf("author%d", i)] = 1
			}
			e.VectorClock = vc
		}, "entries (max 100)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			require.NoError(t, runCreate("own node", "", "", 3, "", "", "", "", ""))
			own, err := readPendingBatch(ctx, app.store, 10)
			require.NoError(t, err)
			require.Len(t, own, 1)
			lamportBefore, vcBefore := peerLamport(t), peerVectorClock(t)
			tampered := *own[0]
			tt.tamper(&tampered)
			hub := &fakeLateHub{pullEvents: []*model.SyncEvent{&tampered}, hubNows: []time.Time{sweepHubT1}}
			var stderr bytes.Buffer

			_, err = pullThenSweep(ctx, testIngest(&stderr), hub, app.store, transport.PullCursor{}, 100)

			require.NoError(t, err)
			requireQuarantinedAs(t, &tampered, "pull", tt.wantReason)
			require.Equal(t, lamportBefore, peerLamport(t), "the local Lamport clock does not move")
			require.Equal(t, vcBefore, peerVectorClock(t), "the local vector clock does not move")
			require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, tampered.EventID))
			require.NoError(t, runCreate("later write", "", "", 3, "", "", "", "", ""), "later local writes work")
			pending, err := readPendingBatch(ctx, app.store, 10)
			require.NoError(t, err)
			require.Len(t, pending, 2)
			require.NoError(t, validator.ValidateBatch(pending, time.Now().UTC(), nil),
				"the next push passes the hub's validation")
		})
	}
}

// TestPull_OwnEventHubCopyUntouched_Acknowledged: an unchanged hub copy of an
// own event passes the checks and is acknowledged (recorded in
// applied_events) without being applied again.
func TestPull_OwnEventHubCopyUntouched_Acknowledged(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, runCreate("own node", "", "", 3, "", "", "", "", ""))
	own, err := readPendingBatch(ctx, app.store, 10)
	require.NoError(t, err)
	hub := &fakeLateHub{pullEvents: own, hubNows: []time.Time{sweepHubT1}}

	got, err := pullThenSweep(ctx, testIngest(nil), hub, app.store, transport.PullCursor{}, 100)

	require.NoError(t, err)
	require.Equal(t, 1, got.pulled)
	require.Empty(t, quarantined(t))
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, own[0].EventID))
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE event_id = ?`, own[0].EventID),
		"no second sync_events row")
}
