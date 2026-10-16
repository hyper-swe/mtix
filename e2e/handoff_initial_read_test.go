// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// These tests verify that an initial durable handoff read outlives the local
// wait deadline while remaining interruptible by caller cancellation.
package e2e

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

type initialReadResult struct {
	events []sqlite.InboxEvent
	err    error
}

func pendingHandoffStore(t *testing.T) *fakeCLI {
	t.Helper()
	host := newFakeCLI(t, "planner", "cccccccccccccccc")
	host.createNode(t, "FLOW-1", "committed handoff")
	thread := &commentThread{cli: host, nodeID: "FLOW-1"}
	thread.post(t, "planner", "developer", "already committed")
	pending, err := host.store.InboxList(t.Context(), "developer")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	return host
}

func blockHandoffInitialRead(ctx context.Context, t *testing.T, host *fakeCLI) (<-chan initialReadResult, func()) {
	t.Helper()
	pool := host.store.ReadDB()
	pool.SetMaxOpenConns(1)
	conn, err := pool.Conn(t.Context())
	require.NoError(t, err)
	t.Cleanup(func() {
		// Explicit release may already have closed the held connection.
		if closeErr := conn.Close(); closeErr != nil {
			require.ErrorIs(t, closeErr, sql.ErrConnDone)
		}
	})
	before := pool.Stats().WaitCount
	out := make(chan initialReadResult, 1)
	go func() {
		events, err := host.store.InboxWait(ctx, "developer", 10*time.Millisecond)
		out <- initialReadResult{events, err}
	}()
	require.Eventually(t, func() bool { return pool.Stats().WaitCount > before },
		5*time.Second, time.Millisecond, "initial query reaches the occupied real read pool")
	// This is intentional deadline injection, not delivery synchronization.
	<-time.After(25 * time.Millisecond)
	return out, func() { require.NoError(t, conn.Close()) }
}

func awaitInitialRead(t *testing.T, out <-chan initialReadResult) initialReadResult {
	t.Helper()
	select {
	case result := <-out:
		return result
	case <-time.After(5 * time.Second):
		t.Fatal("initial read did not finish after release or cancellation")
		return initialReadResult{}
	}
}

func TestScenario_HandoffInitialRead_PendingSurvivesLocalDeadline(t *testing.T) {
	host := pendingHandoffStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	out, release := blockHandoffInitialRead(ctx, t, host)
	release()
	result := awaitInitialRead(t, out)
	pending, err := host.store.InboxList(ctx, "developer")
	require.NoError(t, err)
	require.Len(t, pending, 1, "the committed event remains durably eligible after the delayed read")
	t.Logf("durable inbox after local deadline=%+v initial result=%+v error=%v", pending, result.events, result.err)
	require.NoError(t, result.err)
	require.Len(t, result.events, 1, "local wait expiration cannot erase an already-committed handoff")
	require.Equal(t, "already committed", result.events[0].Body)
	require.Equal(t, pending, result.events, "wait returns the actual durable event without acking it")
}

func TestScenario_HandoffInitialRead_ParentCancellationSurvivesLocalDeadline(t *testing.T) {
	host := pendingHandoffStore(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out, release := blockHandoffInitialRead(ctx, t, host)
	cancel()
	result := awaitInitialRead(t, out)
	release()
	require.ErrorIs(t, result.err, context.Canceled)
	require.Empty(t, result.events)
}
