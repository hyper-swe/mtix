// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// Guards of the clone's orphan quarantine (MTIX-95.37.1): the behind-
// quarantined-create rule of pull, what counts as an orphan, the fresh and
// resumed resets, the already-quarantined skip and the end-of-clone retry.

func orphanDelete(t *testing.T, nodeID string, lamport int64) *model.SyncEvent {
	t.Helper()
	eid, err := clock.NewEventID()
	require.NoError(t, err)
	e := remoteCreateAt(t, nodeID, lamport)
	e.EventID, e.UID, e.OpType, e.Payload = eid, "", model.OpDelete, []byte(`{}`)
	return e
}

func nodeDeleted(t *testing.T, id string) bool {
	t.Helper()
	return countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE id = ? AND deleted_at IS NOT NULL`, id) == 1
}

// TestCheckThenClone_DeleteBehindQuarantinedChildCreate_ParentArrives: the
// child create is quarantined (its parent comes later), the teammate's delete
// of the child waits behind it instead of applying as a no-op, and once the
// parent arrives the retry applies the create and then the delete: the task
// is not resurrected.
func TestCheckThenClone_DeleteBehindQuarantinedChildCreate_ParentArrives(t *testing.T) {
	initTestApp(t)
	child := childCreate(t, "TEST-9.1", "TEST-9", 2)
	del := orphanDelete(t, "TEST-9.1", 3)
	parent := remoteCreateAt(t, "TEST-9", 4)
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{child, del, parent}}

	_, _, stage, err := checkThenClone(context.Background(), &bytes.Buffer{}, hub, transport.PullCursor{}, 10)

	require.NoError(t, err, stage)
	require.Empty(t, quarantined(t), "the retry applied both")
	require.True(t, nodeDeleted(t, "TEST-9.1"), "the delete applied after the creation")
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, del.EventID))
}

// TestApplyBatch_DeleteBehindQuarantinedChildCreate_QuarantinedNotApplied:
// while the parent is missing, the delete is quarantined behind the create
// with the clone-origin marker, recorded as applied nowhere, and
// acknowledgeable.
func TestApplyBatch_DeleteBehindQuarantinedChildCreate_QuarantinedNotApplied(t *testing.T) {
	initTestApp(t)
	child := childCreate(t, "TEST-9.1", "TEST-9", 2)
	del := orphanDelete(t, "TEST-9.1", 3)

	require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, []*model.SyncEvent{child, del}))

	requireNotApplied(t, del)
	rows := quarantined(t)
	require.Len(t, rows, 2)
	require.Contains(t, rows[del.EventID].Reason, sqlite.CloneOrphanReason)
	require.Contains(t, rows[del.EventID].Reason, "depends on a quarantined create")
	require.True(t, service.QuarantineAcknowledgementEligible(rows[del.EventID]))
}

func TestIsOrphanFailure_ClassifiesOnlyDependentsOfAbsentTasks(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{remoteCreateAt(t, "TEST-1", 1)}))
	root := remoteCreateAt(t, "TEST-2", 2)
	absent := childCreate(t, "TEST-9.1", "TEST-9", 3)
	present := childCreate(t, "TEST-1.1", "TEST-1", 4)
	garbled := childCreate(t, "TEST-9.2", "TEST-9", 5)
	garbled.Payload = []byte(`{"parent_id":"TEST-9","title":5}`) // decode error with the parent still filled
	claim := orphanClaim(t, "TEST-9.3", 6)
	fault := errors.New("insert failed")
	tests := []struct {
		name string
		e    *model.SyncEvent
		err  error
		want bool
	}{
		{"not found on a claim", claim, fmt.Errorf("node: %w", model.ErrNotFound), true},
		{"other fault on a claim", claim, fault, false},
		{"create with an absent parent", absent, fault, true},
		{"create with a present parent", present, fault, false},
		{"root create fault", root, fault, false},
		{"create with an undecodable payload", garbled, fault, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
				got, err := isOrphanFailure(ctx, tx, tt.e, tt.err)
				require.NoError(t, err)
				require.Equal(t, tt.want, got)
				return nil
			}))
		})
	}
	t.Run("failed parent lookup is an error, not an orphan", func(t *testing.T) {
		cancelled, cancel := context.WithCancel(ctx)
		require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
			cancel()
			got, err := isOrphanFailure(cancelled, tx, absent, fault)
			require.Error(t, err)
			require.False(t, got)
			return nil
		}))
	})
}

// TestResetCloneState_FreshClearsAllResumeKeepsOrphans: a fresh clone starts
// from an empty quarantine and sweep state; a resumed one clears the sweep
// state and the plain pulled rows but keeps the clone orphans.
func TestResetCloneState_FreshClearsAllResumeKeepsOrphans(t *testing.T) {
	ctx := context.Background()
	seed := func(t *testing.T) *model.SyncEvent {
		initTestApp(t)
		orphan := orphanClaim(t, "TEST-9.1", 2)
		require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{orphan}))
		plain := quarantineRowFor("plain", "apply failed")
		require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error { return sqlite.QuarantineEvent(ctx, tx, plain) }))
		setLastSweep(t, "2026-09-24T09:00:00Z")
		return orphan
	}
	t.Run("fresh", func(t *testing.T) {
		seed(t)
		require.NoError(t, resetCloneState(ctx, app.store, transport.PullCursor{}))
		require.Empty(t, quarantined(t))
		require.Empty(t, lastSweep(t))
	})
	t.Run("resume", func(t *testing.T) {
		orphan := seed(t)
		require.NoError(t, resetCloneState(ctx, app.store, transport.PullCursor{Lamport: 1, EventID: "e"}))
		rows := quarantined(t)
		require.Len(t, rows, 1)
		require.Contains(t, rows, orphan.EventID)
		require.Empty(t, lastSweep(t))
	})
}

func quarantineRowFor(id, reason string) sqlite.QuarantinedEvent {
	return sqlite.QuarantinedEvent{
		EventID: id, Source: "pull", RawEvent: `{"lamport_clock":1}`, Reason: reason,
		FirstSeen: "2026-09-25T10:00:00Z", LastAttempt: "2026-09-25T10:00:00Z",
	}
}

// TestApplyBatchReport_BatchReadAgain_SkipsQuarantinedEvent: after a crash
// between a batch's commit and its checkpoint, the resume reads the batch
// again; the quarantined event is neither recounted nor reported again.
func TestApplyBatchReport_BatchReadAgain_SkipsQuarantinedEvent(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	orphan := orphanClaim(t, "TEST-9.1", 2)

	first, err := applyBatchReport(ctx, testIngest(nil), app.store, []*model.SyncEvent{orphan})
	require.NoError(t, err)
	again, err := applyBatchReport(ctx, testIngest(nil), app.store, []*model.SyncEvent{orphan})

	require.NoError(t, err)
	require.Len(t, first, 1)
	require.Empty(t, again, "not reported a second time")
	rows := quarantined(t)
	require.Len(t, rows, 1)
	require.Equal(t, 1, rows[orphan.EventID].Attempts)
}

// TestCloneLoop_EndRetryFails_SavesNoPullCursor: the end-of-clone retry runs
// before the pull cursor is written, and its failure fails the clone with no
// cursor saved. A trigger makes the retry's attempt count write fail.
func TestCloneLoop_EndRetryFails_SavesNoPullCursor(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	execPeer(t, `CREATE TRIGGER fail_quarantine_update BEFORE UPDATE ON sync_quarantine
		BEGIN SELECT RAISE(ABORT, 'retry write refused'); END`)
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{orphanClaim(t, "TEST-9.1", 2)}}

	_, _, err := cloneLoop(ctx, &bytes.Buffer{}, hub, app.store, transport.PullCursor{}, 10)

	require.ErrorContains(t, err, "quarantine retry")
	require.ErrorContains(t, err, "retry write refused")
	cursor, readErr := readLastPulledClock(ctx, app.store)
	require.NoError(t, readErr)
	require.Equal(t, transport.PullCursor{}, cursor, "no pull cursor was saved")
}
