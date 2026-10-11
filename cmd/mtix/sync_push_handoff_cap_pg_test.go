// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

func TestRunSyncPush_ProcessCappedHandoffRecovery(t *testing.T) {
	pool := openCmdHub(t)
	dir := t.TempDir()
	store := newDaemonTestStore(t, dir)
	holder := startPushHandoffChild(t, dir, "cap-holder")
	events := make([]string, 3)
	for n := 1; n <= 3; n++ {
		holder.frame(t, fmt.Sprintf("pass-%d", n))
		contender := startPushHandoffChild(t, dir, fmt.Sprintf("contender-%d", n))
		contender.frame(t, "ready")
		contender.release(t)
		events[n-1] = contender.frame(t, "skipped").EventID
		require.NotEmpty(t, events[n-1])
		contender.join(t)
		holder.release(t)
	}
	holder.frame(t, "done")
	holder.join(t)
	for _, event := range events[:2] {
		handoffNativeDelivery(t, store, pool, event, handoffDeliveryExpected{state: "pushed", hubCount: 1})
	}
	handoffNativeDelivery(t, store, pool, events[2], handoffDeliveryExpected{state: "pending", hubCount: 0})
	status := startPushHandoffChild(t, dir, "status")
	status.frame(t, "ready")
	status.release(t)
	frame := status.frame(t, "status")
	require.NotNil(t, frame.Pending)
	require.Equal(t, 1, *frame.Pending)
	status.join(t)
	handoffNativeDelivery(t, store, pool, events[2], handoffDeliveryExpected{state: "pending", hubCount: 0})
	later := startPushHandoffChild(t, dir, "later-push")
	later.frame(t, "ready")
	later.release(t)
	later.frame(t, "done")
	later.join(t)
	handoffNativeDelivery(t, store, pool, events[2], handoffDeliveryExpected{state: "pushed", hubCount: 1})
}

type handoffDeliveryExpected struct {
	state    string
	hubCount int
}

func handoffNativeDelivery(t *testing.T, store *sqlite.Store, pool *transport.Pool, event string, expected handoffDeliveryExpected) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var local string
	// Inspect only the real committed event, without any mutation or push.
	require.NoError(t, store.WriteDB().QueryRowContext(ctx, `SELECT sync_status FROM sync_events WHERE event_id = ?`, event).Scan(&local))
	var delivered int
	// Assert real hub delivery for this event using a bound identifier value.
	require.NoError(t, pool.Inner().QueryRow(ctx, `SELECT COUNT(*) FROM sync_events WHERE event_id = $1`, event).Scan(&delivered))
	t.Logf("native cap event=%s local=%s hub_count=%d", event, local, delivered)
	require.Equal(t, expected.state, local)
	require.Equal(t, expected.hubCount, delivered)
}
