// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// The tuple pull cursor against a real hub (MTIX-95.4; ADR-006 D6, D16):
// the cursor pass, the clone and a daemon pull tick run their real code
// against MTIX_PG_TEST_DSN (the harness of sync_loops_pg_test.go). The
// cursor-pass tests call pullLoop, not runSyncPull, because a full pull also
// runs the late-event sweep, which would deliver a skipped event and hide
// the cursor pass's own behaviour. They are named TestCloudPath_* so the
// cloud-contract gate's cmd/mtix filter runs them on every provider shard.

// pushToHub pushes events straight to the hub through the transport pool,
// bypassing the local store, and requires the hub to accept every one.
func pushToHub(t *testing.T, pool *transport.Pool, events ...*model.SyncEvent) {
	t.Helper()
	accepted, _, err := pool.PushEvents(context.Background(), events)
	require.NoError(t, err)
	require.Len(t, accepted, len(events), "the hub accepts every event")
}

// requireHubOrder asserts the hub serves events in exactly this order, so a
// test's expected cursor is the hub's own last event.
func requireHubOrder(t *testing.T, pool *transport.Pool, events []*model.SyncEvent) {
	t.Helper()
	got, hasMore, err := pool.PullEvents(context.Background(), transport.PullCursor{}, len(events)+1)
	require.NoError(t, err)
	require.False(t, hasMore)
	require.Equal(t, eventIDs(events), eventIDs(got), "hub keyset order")
}

// TestCloudPath_Pull_EqualLamportAcrossBatches_AllApplied: the real cursor
// pass with --limit 2 against a hub holding three events that share one
// Lamport clock applies all three and saves the cursor at the hub's last
// event.
func TestCloudPath_Pull_EqualLamportAcrossBatches_AllApplied(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	events := remoteCreates(t, 5, 5, 5)
	pushToHub(t, pool, events...)
	requireHubOrder(t, pool, events)
	var stderr bytes.Buffer

	applied, batches, err := pullLoop(context.Background(), testIngest(&stderr), pool, app.store,
		transport.PullCursor{}, 2)

	require.NoError(t, err, stderr.String())
	require.Equal(t, 3, applied)
	require.Equal(t, 2, batches)
	for _, e := range events {
		requireAppliedOnce(t, e)
	}
	requireSavedCursor(t, transport.CursorAt(events[2]))
}

// TestCloudPath_Pull_LamportOnlyCursor_ReReadsBoundaryIdempotently: a store
// upgraded from a Lamport-only cursor, whose old pull applied two of the
// three events at its clock, reads that clock again from the real hub: the
// skipped event is applied, the two it holds are deduplicated, and nothing
// logs a conflict.
func TestCloudPath_Pull_LamportOnlyCursor_ReReadsBoundaryIdempotently(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	events := remoteCreates(t, 5, 5, 5, 6)
	pushToHub(t, pool, events...)
	requireHubOrder(t, pool, events)
	held, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourceSweep, events[:2])
	require.NoError(t, err)
	require.Empty(t, held)
	setPeerMeta(t, "meta.sync.last_pulled_clock", "5")
	execPeer(t, `DELETE FROM meta WHERE key = 'meta.sync.last_pulled_event_id'`)
	before := nodeSnapshot(t, events[0].NodeID, events[1].NodeID)

	cursor, err := readLastPulledClock(ctx, app.store)
	require.NoError(t, err)
	require.Equal(t, transport.PullCursor{Lamport: 5}, cursor)
	var stderr bytes.Buffer
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, cursor, 2)

	require.NoError(t, err, stderr.String())
	for _, e := range events {
		requireAppliedOnce(t, e)
	}
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_conflicts`))
	require.Equal(t, before, nodeSnapshot(t, events[0].NodeID, events[1].NodeID))
	requireSavedCursor(t, transport.CursorAt(events[3]))
}

// TestCloudPath_Clone_SetsPullCursor_FirstPullFetchesOnlyNewer: `mtix sync
// clone --batch-size 2` against a hub where three events share a Lamport
// clock clones all three and saves the pull cursor at the last one; the
// first `mtix sync pull` after it applies only the event pushed since.
func TestCloudPath_Clone_SetsPullCursor_FirstPullFetchesOnlyNewer(t *testing.T) {
	dsn := requireCmdPG(t)
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	events := remoteCreates(t, 5, 5, 5)
	pushToHub(t, pool, events...)
	requireHubOrder(t, pool, events)
	var stdout, stderr bytes.Buffer

	require.NoError(t, runSyncClone(ctx, &stdout, &stderr, []string{dsn}, cloudOpts, false, 2), stderr.String())

	require.Contains(t, stdout.String(), "clone complete: 3 events applied across 2 batches")
	for _, e := range events {
		requireAppliedOnce(t, e)
	}
	requireSavedCursor(t, transport.CursorAt(events[2]))

	newer := remoteCreateAt(t, "TEST-4", 6)
	pushToHub(t, pool, newer)
	stdout.Reset()
	stderr.Reset()
	require.NoError(t, runSyncPull(ctx, &stdout, &stderr, []string{dsn}, cloudOpts, 100), stderr.String())

	require.Contains(t, stdout.String(), "pull complete: 1 events applied across 1 batches",
		"the first pull after the clone fetches only the newer event")
	requireAppliedOnce(t, newer)
	requireSavedCursor(t, transport.CursorAt(newer))
}

// TestCloudPath_Daemon_PullTickSavesTupleCursor: a daemon pull tick runs the
// same pull, so it saves both halves of the cursor, and its next tick asks
// the hub only for the events after the saved event.
func TestCloudPath_Daemon_PullTickSavesTupleCursor(t *testing.T) {
	dsn := requireCmdPG(t)
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	events := remoteCreates(t, 5, 5, 5)
	pushToHub(t, pool, events...)
	requireHubOrder(t, pool, events)
	var stderr bytes.Buffer

	runOneDaemonPull(ctx, &stderr, []string{dsn}, cloudOpts)

	for _, e := range events {
		requireAppliedOnce(t, e)
	}
	requireSavedCursor(t, transport.CursorAt(events[2]))

	newer := remoteCreateAt(t, "TEST-4", 6)
	pushToHub(t, pool, newer)
	stderr.Reset()
	runOneDaemonPull(ctx, &stderr, []string{dsn}, cloudOpts)

	require.Contains(t, stderr.String(), "pull progress: batch 1 (1 events, 0 quarantined;",
		"the next tick reads only the event after the saved one")
	requireAppliedOnce(t, newer)
}
