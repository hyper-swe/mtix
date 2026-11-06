// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func wakeRetryFixture(t *testing.T) (*discardPullWakeRig, *model.SyncEvent) {
	t.Helper()
	initTestApp(t)
	orphan := orphanClaim(t, "TEST-5", 2)
	require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{orphan}))
	require.Len(t, quarantined(t), 1)
	create := remoteCreateAt(t, "TEST-5", 3)
	require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{create}))
	f := newDiscardPullWakeRig(t)
	require.NoError(t, app.store.AdvanceWakeScanCursor(t.Context(), f.host, f.uid, 1, time.Minute))
	_, err := app.store.AcquireWakeLease(t.Context(), sqlite.WakeLeaseKey{AgentID: "worker", HostID: f.host, UID: f.uid}, "", time.Minute)
	require.NoError(t, err)
	require.Equal(t, []int64{1, 1, 1}, cloneWakeMarks(t, f))
	require.NoError(t, app.promptSvc.AddAnnotation(t.Context(), "TEST-5", "pending before retry", "sender", "worker"))
	return f, orphan
}

func TestPullRetry_NonEmptyJournal_PreservesWakeAndHookProgress(t *testing.T) {
	f, orphan := wakeRetryFixture(t)
	before, hookBefore := cloneWakeMarks(t, f), hookFloorForClone(t)
	t.Setenv(transport.EnvDSN, "postgres://127.0.0.1:1/unused?sslmode=disable")
	var stdout, stderr bytes.Buffer
	err := runSyncPull(t.Context(), &stdout, &stderr, nil, cloudOpts, 2)
	require.ErrorContains(t, err, "connect")
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id=?`, orphan.EventID))
	require.Empty(t, quarantined(t), "the real retry applied the formerly missing-node event")
	assert.Equal(t, before, cloneWakeMarks(t, f), "quarantine retry must not seed a nonempty journal")
	assert.Equal(t, hookBefore, hookFloorForClone(t))
	require.NoError(t, f.scanner.Scan(t.Context()))
	assert.Equal(t, []int64{2}, f.sent, "unseen addressed mail remains wake eligible")
}

func TestPullRetry_UnreadableTail_DoesNotSeedWakeAndHookProgress(t *testing.T) {
	f, orphan := wakeRetryFixture(t)
	before, hookBefore := cloneWakeMarks(t, f), hookFloorForClone(t)
	app.store.ReadDB().SetMaxOpenConns(1)
	_, err := app.store.ReadDB().ExecContext(t.Context(), `CREATE TEMP VIEW sync_events AS
  SELECT *, abs(-9223372036854775808) AS rowid FROM main.sync_events`)
	require.NoError(t, err)
	_, err = app.store.JournalTail(t.Context())
	require.ErrorContains(t, err, "integer overflow", "fault only the reader's tail expression")
	t.Setenv(transport.EnvDSN, "postgres://127.0.0.1:1/unused?sslmode=disable")
	var stdout, stderr bytes.Buffer
	err = runSyncPull(t.Context(), &stdout, &stderr, nil, cloudOpts, 2)
	require.ErrorContains(t, err, "connect", "retry must apply before hub failure despite the tail fault")
	_, err = app.store.ReadDB().ExecContext(t.Context(), `DROP VIEW temp.sync_events`)
	require.NoError(t, err)
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id=?`, orphan.EventID))
	require.Empty(t, quarantined(t))
	assert.Equal(t, before, cloneWakeMarks(t, f), "unknown starting tail must never authorize bootstrap")
	assert.Equal(t, hookBefore, hookFloorForClone(t))
}
