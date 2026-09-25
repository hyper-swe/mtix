// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Fault injection on the quarantine's error paths (MTIX-95.11 round 3): a
// local fault in the quarantine or the local clock fails the pull at the
// stage that met it, and status and doctor report it instead of passing.

// dropQuarantineTable makes the quarantine unreadable in the peer store.
func dropQuarantineTable(t *testing.T) {
	t.Helper()
	_, err := app.store.WriteDB().ExecContext(context.Background(), `DROP TABLE sync_quarantine`)
	require.NoError(t, err)
}

// TestRunSyncPull_QuarantineUnreadable_FailsAtQuarantineRetry: the retry at
// the start of a pull cannot read the quarantine, so the pull fails at the
// "quarantine retry" stage before the hub is contacted.
func TestRunSyncPull_QuarantineUnreadable_FailsAtQuarantineRetry(t *testing.T) {
	initTestApp(t)
	dropQuarantineTable(t)
	t.Setenv(transport.EnvDSN, "")

	err := runSyncPull(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}, nil, transport.Options{}, 100)

	require.ErrorContains(t, err, "mtix sync quarantine retry:")
	require.ErrorContains(t, err, "sync_quarantine")
}

// TestPullThenSweep_EndRetryFails_ReturnsQuarantineRetryStage: when the
// retry at the end of a pull fails (here a trigger refuses the attempt
// count of a still-failing quarantined event), the pull returns that error
// with the "quarantine retry" stage; the cursor pass's work stays.
func TestPullThenSweep_EndRetryFails_ReturnsQuarantineRetryStage(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	_, _, link := linkDepEvents(t)
	_, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull, []*model.SyncEvent{link})
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx, `
		CREATE TRIGGER refuse_attempt BEFORE UPDATE ON sync_quarantine
		BEGIN SELECT RAISE(ABORT, 'injected quarantine fault'); END`)
	require.NoError(t, err)
	hub := &fakeLateHub{pullEvents: events[:1], hubNows: []time.Time{sweepHubT1}}

	got, err := pullThenSweep(ctx, testIngest(nil), hub, app.store, 0, 100)

	require.ErrorContains(t, err, "injected quarantine fault")
	require.Equal(t, "quarantine retry", got.stage)
	require.Equal(t, 1, got.pulled, "the cursor pass committed before the retry")
}

// TestPullLoop_LocalClockUnreadableAfterBatch_FailsPullLoop: reading the
// local clock after a committed batch (for the cursor decision) fails, so
// the pull fails at the "pull loop" stage and no cursor is saved from it.
func TestPullLoop_LocalClockUnreadableAfterBatch_FailsPullLoop(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	events := offlineEvents(t)
	_, err := app.store.WriteDB().ExecContext(ctx, `
		CREATE TRIGGER corrupt_clock AFTER INSERT ON applied_events
		BEGIN UPDATE meta SET value = 'x' WHERE key = 'meta.sync.lamport'; END`)
	require.NoError(t, err)
	hub := &fakeLateHub{pullEvents: events[:1], hubNows: []time.Time{sweepHubT1}}

	got, err := pullThenSweep(ctx, testIngest(nil), hub, app.store, 0, 100)

	require.ErrorContains(t, err, "read local clock after batch 1")
	require.ErrorContains(t, err, "meta.sync.lamport")
	require.Equal(t, "pull loop", got.stage)
	requireCursor(t, 0, "no cursor is saved from a batch whose cursor decision failed")
}

// TestRunSyncDoctorAndStatus_QuarantineUnreadable_ReportFailure: when the
// quarantine cannot be read, doctor's check fails with the error instead
// of passing, and status returns the error instead of a zero count.
func TestRunSyncDoctorAndStatus_QuarantineUnreadable_ReportFailure(t *testing.T) {
	initTestApp(t)
	t.Setenv(transport.EnvDSN, "")
	dropQuarantineTable(t)
	ctx := context.Background()
	var out bytes.Buffer

	app.jsonOutput = true
	require.ErrorIs(t, runSyncDoctor(ctx, &out, &bytes.Buffer{}, nil, transport.Options{}), errDoctorChecksFailed)
	check := doctorCheckNamed(t, out.Bytes(), "quarantined events")
	require.False(t, check.Pass)
	require.Contains(t, check.Detail, "count quarantined events")

	out.Reset()
	err := runSyncStatus(ctx, &out, &bytes.Buffer{})
	require.ErrorContains(t, err, "count quarantined events")
	var printed map[string]any
	require.Error(t, json.Unmarshal(out.Bytes(), &printed), "no status is printed")
}
