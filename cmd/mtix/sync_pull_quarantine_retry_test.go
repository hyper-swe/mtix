// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Retry of the quarantine (MTIX-95.11 round 2): a row whose event this
// store already holds is dropped before any check, and a first fill that
// comes only from the start-of-pull retry initializes the hook scan floor.

// TestRetryQuarantinedEvents_AlreadyApplied_DroppedOwnEventChecked: a
// quarantine row whose event_id is already in applied_events (for example
// after a clone applied it) is removed before the checks run, even though
// its raw event would still fail them. A row for this replica's own event,
// only in sync_events, is not: its hub copy is checked like any other
// (MTIX-95.11 round 4), so a copy that fails the checks stays quarantined
// with the attempt counted.
func TestRetryQuarantinedEvents_AlreadyApplied_DroppedOwnEventChecked(t *testing.T) {
	tests := []struct {
		name string
		hold func(t *testing.T) string
		want quarantineRetry
	}{
		{"applied_events", func(t *testing.T) string {
			e := offlineEvents(t)[0]
			_, err := app.store.WriteDB().ExecContext(context.Background(),
				`INSERT INTO applied_events (event_id, applied_at, applied_by_lamport) VALUES (?, 't', 1)`, e.EventID)
			require.NoError(t, err)
			return e.EventID
		}, quarantineRetry{dropped: 1}},
		{"sync_events only (own event)", func(t *testing.T) string {
			require.NoError(t, runCreate("own node", "", "", 3, "", "", "", "", ""))
			own, err := readPendingBatch(context.Background(), app.store, 10)
			require.NoError(t, err)
			require.Len(t, own, 1)
			return own[0].EventID
		}, quarantineRetry{held: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			ctx := context.Background()
			id := tt.hold(t)
			bad := *offlineEvents(t)[0]
			bad.EventID, bad.Payload = id, oversizedPayload()
			raw, err := json.Marshal(&bad)
			require.NoError(t, err)
			require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
				return sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{EventID: id, Source: "pull",
					RawEvent: string(raw), Reason: "payload too large", FirstSeen: "t", LastAttempt: "t"})
			}))
			var stderr bytes.Buffer

			got, err := retryQuarantinedEvents(ctx, testIngest(&stderr), app.store, 100)

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
			if tt.want.dropped == 1 {
				require.Empty(t, quarantined(t))
				require.Contains(t, stderr.String(), "1 quarantined events already applied locally; removed from the quarantine")
				return
			}
			require.Equal(t, 2, quarantined(t)[id].Attempts, "checked and still refused")
		})
	}
}

// TestRunSyncPull_RetryFirstFill_InitializesHookScanFloor: on a store whose
// journal is empty, events that the start-of-pull retry applies are
// history like any first fill, so the hook scan floor moves to the journal
// tail, even when the pull then fails to reach the hub.
func TestRunSyncPull_RetryFirstFill_InitializesHookScanFloor(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	strict := testIngest(nil)
	strict.maxJump = 1
	far := *events[0]
	far.LamportClock = 5
	held, err := applyPullBatch(ctx, strict, app.store, quarantineSourcePull, []*model.SyncEvent{&far})
	require.NoError(t, err)
	require.Len(t, held, 1, "precondition: refused by the strict bound")
	tail, err := app.store.JournalTail(ctx)
	require.NoError(t, err)
	require.Zero(t, tail, "precondition: an empty journal")
	t.Setenv(transport.EnvDSN, "")

	err = runSyncPull(ctx, &bytes.Buffer{}, &bytes.Buffer{}, nil, transport.Options{}, 100)

	require.ErrorContains(t, err, "mtix sync dsn:")
	tail, err = app.store.JournalTail(ctx)
	require.NoError(t, err)
	require.Positive(t, tail, "the retry applied the event")
	var floor int64
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT cursor FROM hook_dispatch_cursor WHERE id = 1`).Scan(&floor))
	require.Equal(t, tail, floor, "the hook scan floor starts at the tail")
}

// TestResetPullState_ClearsQuarantineAndSweep: `mtix sync clone` resets the
// late-event sweep state and empties the quarantine, since it rebuilds the
// store from the hub.
func TestResetPullState_ClearsQuarantineAndSweep(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	quarantineN(t, 2)
	setLastSweep(t, "2026-09-24T09:00:00Z")

	require.NoError(t, resetPullState(ctx, app.store))

	require.Empty(t, quarantined(t))
	require.Equal(t, "", lastSweep(t))
}
