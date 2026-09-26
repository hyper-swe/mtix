// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Held push events share sync_quarantine with quarantined pulled events
// (MTIX-95.12). These tests pin that the pull side leaves them alone and
// that `mtix sync quarantine list` shows them with source push.

// quarantinePulled stores one quarantined pulled event.
func quarantinePulled(t *testing.T, id string) {
	t.Helper()
	ctx := context.Background()
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		return sqlite.QuarantineEvent(ctx, tx, sqlite.QuarantinedEvent{
			EventID: id, Source: "pull", RawEvent: `{"lamport_clock":1}`, Reason: "r",
			FirstSeen: "2026-09-25T00:00:00Z", LastAttempt: "2026-09-25T00:00:00Z",
		})
	}))
}

// TestRetryQuarantinedEvents_HeldPushEvent_NotRetried: the pull's
// quarantine retry skips a held push event: it is not applied, dropped or
// counted, and its row is untouched.
func TestRetryQuarantinedEvents_HeldPushEvent_NotRetried(t *testing.T) {
	initTestApp(t)
	held := holdOversizedEvents(t, 1)[0]

	var stderr bytes.Buffer
	out, err := retryQuarantinedEvents(context.Background(), testIngest(&stderr), app.store, 100)
	require.NoError(t, err)
	require.Equal(t, quarantineRetry{}, out)
	q, ok := quarantined(t)[held]
	require.True(t, ok)
	require.Equal(t, "push", q.Source)
	require.Equal(t, 1, q.Attempts, "the retry does not count an attempt")
	require.Equal(t, "pending", syncStatusOf(t, held))
}

// TestResetPullState_HeldPushEvent_Kept: the reset `mtix sync clone` runs
// removes quarantined pulled events but keeps held push events.
func TestResetPullState_HeldPushEvent_Kept(t *testing.T) {
	initTestApp(t)
	held := holdOversizedEvents(t, 1)[0]
	quarantinePulled(t, "0193fb00-0000-7000-8000-0000000000aa")

	require.NoError(t, resetPullState(context.Background(), app.store))
	rows := quarantined(t)
	require.Len(t, rows, 1)
	require.Contains(t, rows, held)
}

// TestRunSyncQuarantineList_HeldPushEvent_ShownWithSourcePush: the list
// shows a held push event with its node, op and reason, and --json gives
// its source as push.
func TestRunSyncQuarantineList_HeldPushEvent_ShownWithSourcePush(t *testing.T) {
	initTestApp(t)
	held := holdOversizedEvents(t, 1)[0]
	ctx := context.Background()

	var out bytes.Buffer
	app.jsonOutput = true
	require.NoError(t, runSyncQuarantineList(ctx, &out))
	var events []service.QuarantinedEvent
	require.NoError(t, json.Unmarshal(out.Bytes(), &events))
	require.Len(t, events, 1)
	require.Equal(t, held, events[0].EventID)
	require.Equal(t, "push", events[0].Source)
	require.Equal(t, "TEST-1", events[0].NodeID)
	require.Equal(t, "create_node", events[0].OpType)
	require.True(t, strings.HasPrefix(events[0].Reason, "too large: payload "), events[0].Reason)

	out.Reset()
	app.jsonOutput = false
	require.NoError(t, runSyncQuarantineList(ctx, &out))
	require.Contains(t, out.String(), held)
	require.Contains(t, out.String(), "the prompt field is largest")
}
