// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestPull_CreateWhoseUIDALocalTaskHolds_QuarantinedThenAppliedAfterRepair
// verifies a pulled create whose uid another local task holds is not
// dropped (MTIX-95.31.8): the pull quarantines it with the uid and holder
// named, it shows in the quarantine and the doctor, and the next pull
// applies it once the local task no longer holds the uid.
func TestPull_CreateWhoseUIDALocalTaskHolds_QuarantinedThenAppliedAfterRepair(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, app.store.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "local task")))
	var localUID string
	require.NoError(t, app.store.QueryRow(ctx, `SELECT uid FROM nodes WHERE id = 'TEST-1'`).Scan(&localUID))

	theirs := *offlineEvents(t)[0] // create of TEST-9
	theirs.UID = localUID
	hub := &fakeLateHub{events: []*model.SyncEvent{&theirs}, pullEvents: []*model.SyncEvent{&theirs},
		hubNows: []time.Time{sweepHubT1}}
	var stderr bytes.Buffer

	_, err := pullThenSweep(ctx, testIngest(&stderr), hub, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	requireNotApplied(t, &theirs)
	requireQuarantinedAs(t, &theirs, "pull", "uid "+localUID+" is held by local task TEST-1")

	var out, errOut bytes.Buffer
	app.jsonOutput = true
	_ = runSyncDoctor(ctx, &out, &errOut, nil, transport.Options{})
	assert.False(t, doctorCheckFromJSON(t, out.Bytes(), quarantineCheckName).Pass)

	// The owner repairs the duplicate; the next pull retries the event.
	_, err = app.store.WriteDB().ExecContext(ctx, `UPDATE nodes SET uid = '01a0fb06-0000-7000-8000-0000000000aa' WHERE id = 'TEST-1'`)
	require.NoError(t, err)
	_, err = retryQuarantinedEvents(ctx, testIngest(&stderr), app.store, 100)
	require.NoError(t, err)
	_, err = app.store.GetNode(ctx, "TEST-9")
	require.NoError(t, err, "the held create applied after the repair")
	assert.Empty(t, quarantined(t))
}

// TestRunSyncDoctor_UIDHeldQuarantine_NamesTheAction verifies the doctor's
// quarantine detail names the next step for the uid-held reason (MTIX-95.31.8).
func TestRunSyncDoctor_UIDHeldQuarantine_NamesTheAction(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, app.store.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "local task")))
	var localUID string
	require.NoError(t, app.store.QueryRow(ctx, `SELECT uid FROM nodes WHERE id = 'TEST-1'`).Scan(&localUID))
	theirs := *offlineEvents(t)[0]
	theirs.UID = localUID
	hub := &fakeLateHub{events: []*model.SyncEvent{&theirs}, pullEvents: []*model.SyncEvent{&theirs},
		hubNows: []time.Time{sweepHubT1}}
	_, err := pullThenSweep(ctx, testIngest(nil), hub, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)

	ok, detail := checkQuarantinedEvents(ctx, app.store)
	assert.False(t, ok)
	assert.Contains(t, detail, "is held by local task <id>")
	assert.Contains(t, detail, "escalate to the project maintainer")
}
