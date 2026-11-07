// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestCloneWakeRetry_OrdinaryPullDoesNotContinueCloneBootstrap(t *testing.T) {
	initTestApp(t)
	orphan := orphanClaim(t, "TEST-5", 2)
	require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{orphan}))
	create := remoteCreateAt(t, "TEST-5", 3)
	require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{create}))
	f := wakeRigAtTail(t, 1)
	t.Setenv(transport.EnvDSN, "postgres://127.0.0.1:1/unused?sslmode=disable")
	var stdout, stderr bytes.Buffer
	require.ErrorContains(t, runSyncPull(t.Context(), &stdout, &stderr, nil, cloudOpts, 2), "connect")
	require.Empty(t, quarantined(t), "ordinary pull really retried the clone-origin event before the hub failure")
	assert.Equal(t, []int64{1, 1, 1}, cloneWakeMarks(t, f))
	assert.Equal(t, int64(1), hookFloorForClone(t), "ordinary nonempty pull keeps its existing floor policy")
}

// Clone-only retry and completion share the durable bootstrap contract.
func TestCloneWakeRetry_OrphanAppliesWithinBootstrap(t *testing.T) {
	initTestApp(t)
	orphan := orphanClaim(t, "TEST-5", 2)
	require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{orphan}))
	require.Len(t, quarantined(t), 1)
	create := remoteCreateAt(t, "TEST-5", 3)
	require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{create}))
	f := wakeRigAtTail(t, 1)
	retried, err := retryCloneQuarantinedEvents(t.Context(), app.store, 2)
	require.NoError(t, err)
	require.Equal(t, 1, retried.applied)
	require.Empty(t, quarantined(t))
	assert.Equal(t, []int64{2, 2, 2}, cloneWakeMarks(t, f))
	assert.Equal(t, int64(2), hookFloorForClone(t))
}

func TestFinishClonePullCursor_WriteFailure_RollsBackCursorAndIntent(t *testing.T) {
	for _, tt := range []struct{ name, trigger string }{
		{"cursor", `CREATE TRIGGER refuse_clone_finish BEFORE INSERT ON meta
		 WHEN NEW.key='meta.sync.last_pulled_clock' BEGIN SELECT RAISE(ABORT,'forced clone completion failure'); END`},
		{"intent", `CREATE TRIGGER refuse_clone_finish BEFORE DELETE ON meta
		 WHEN OLD.key='meta.sync.clone.wake_bootstrap' BEGIN SELECT RAISE(ABORT,'forced clone completion failure'); END`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			e := remoteCreateAt(t, "TEST-1", 1)
			require.NoError(t, applyBatch(t.Context(), testIngest(nil), app.store, []*model.SyncEvent{e}))
			before, err := readLastPulledClock(t.Context(), app.store)
			require.NoError(t, err)
			_, err = app.store.WriteDB().ExecContext(t.Context(), tt.trigger)
			require.NoError(t, err)
			require.ErrorContains(t, finishClonePullCursor(t.Context(), app.store, transport.CursorAt(e)), "forced clone completion failure")
			after, err := readLastPulledClock(t.Context(), app.store)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			var witness string
			require.NoError(t, app.store.ReadDB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='meta.sync.clone.wake_bootstrap'`).Scan(&witness))
			assert.NotEmpty(t, witness)
			_, err = app.store.WriteDB().ExecContext(t.Context(), `DROP TRIGGER refuse_clone_finish`)
			require.NoError(t, err)
			require.NoError(t, finishClonePullCursor(t.Context(), app.store, transport.CursorAt(e)))
			err = app.store.ReadDB().QueryRowContext(t.Context(), `SELECT value FROM meta WHERE key='meta.sync.clone.wake_bootstrap'`).Scan(&witness)
			require.ErrorIs(t, err, sql.ErrNoRows)
		})
	}
}
