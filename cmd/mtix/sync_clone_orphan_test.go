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

// Orphaned dependent events in a clone (MTIX-95.37.1): the hub holds events
// for a task the clone never creates (pushed under an old number by a client
// before 0.5.4). Driven through checkThenClone and applyBatch with fake pullers.

// orphanClaim is a claim of nodeID by a teammate, as a client before 0.5.4
// pushed it: no uid, so apply resolves it by node id.
func orphanClaim(t *testing.T, nodeID string, lamport int64) *model.SyncEvent {
	t.Helper()
	eid, err := clock.NewEventID()
	require.NoError(t, err)
	payload, err := model.EncodePayload(model.ClaimPayload{AgentID: "agent-a"})
	require.NoError(t, err)
	e := remoteCreateAt(t, nodeID, lamport)
	e.EventID, e.UID, e.OpType, e.Payload = eid, "", model.OpClaim, payload
	return e
}

// orphanHistory is a create, a claim of a task never created, and a later create.
func orphanHistory(t *testing.T) (first, orphan, last *model.SyncEvent) {
	t.Helper()
	first = remoteCreateAt(t, "TEST-1", 1)
	orphan = orphanClaim(t, "TEST-9.1", 2)
	last = remoteCreateAt(t, "TEST-2", 3)
	return first, orphan, last
}

func TestCheckThenClone_OrphanDependentEvent_QuarantinedAndRestApplied(t *testing.T) {
	initTestApp(t)
	first, orphan, last := orphanHistory(t)
	var stderr bytes.Buffer
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{first, orphan, last}}

	pulled, batches, stage, err := checkThenClone(context.Background(), &stderr, hub, transport.PullCursor{}, 10)

	require.NoError(t, err, stage)
	require.Equal(t, 3, pulled)
	require.Equal(t, 1, batches)
	requireAppliedOnce(t, first)
	requireAppliedOnce(t, last)
	requireNotApplied(t, orphan)
	q := quarantined(t)[orphan.EventID]
	require.Equal(t, quarantineSourcePull, q.Source)
	require.Contains(t, q.Reason, sqlite.CloneOrphanReason, "the row records the clone origin")
	require.Equal(t, 1, q.Attempts)
	require.Contains(t, stderr.String(), orphan.EventID, "the clone names the quarantined event")
	n, err := countCloneOrphans(context.Background(), app.store)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	requireSavedCursor(t, transport.CursorAt(last))
}

func TestApplyBatch_ApplyFaultOtherThanNotFound_FailsBatchQuarantinesNothing(t *testing.T) {
	initTestApp(t)
	first, orphan, _ := orphanHistory(t)
	in := testIngest(nil)
	in.apply = func(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
		if e.EventID == orphan.EventID {
			return errors.New("disk exploded")
		}
		return sqlite.IdempotentApply(ctx, tx, e)
	}

	err := applyBatch(context.Background(), in, app.store, []*model.SyncEvent{first, orphan})

	require.ErrorContains(t, err, "disk exploded")
	require.Empty(t, quarantined(t), "only a node-not-found event is quarantined")
	requireNotApplied(t, first)
}

func TestApplyBatch_NotFoundApply_RollsBackItsPartialWrites(t *testing.T) {
	initTestApp(t)
	_, orphan, _ := orphanHistory(t)
	in := testIngest(nil)
	in.apply = func(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
		if _, err := tx.ExecContext(ctx, `INSERT INTO meta (key, value) VALUES ('partial.write', '1')`); err != nil {
			return err
		}
		return fmt.Errorf("node %s: %w", e.NodeID, model.ErrNotFound)
	}

	require.NoError(t, applyBatch(context.Background(), in, app.store, []*model.SyncEvent{orphan}))

	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM meta WHERE key = 'partial.write'`),
		"the event's savepoint undoes its writes")
	requireQuarantinedAs(t, orphan, quarantineSourcePull, sqlite.CloneOrphanReason)
}

func TestApplyBatch_RerunAfterInterruption_RecordsEachOrphanOnce(t *testing.T) {
	initTestApp(t)
	first, orphan, last := orphanHistory(t)
	events := []*model.SyncEvent{first, orphan, last}

	require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, events))
	require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, events))

	require.Len(t, quarantined(t), 1)
	require.Equal(t, 1, quarantined(t)[orphan.EventID].Attempts, "a rerun leaves the row as it is")
	requireAppliedOnce(t, first)
	requireAppliedOnce(t, last)
	requireNotApplied(t, orphan)
}

func TestRetryQuarantinedEvents_OrphanWhoseCreateArrives_AppliesAndLeavesQuarantine(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	orphan := orphanClaim(t, "TEST-5", 2)
	require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{orphan}))
	require.Len(t, quarantined(t), 1)

	create := remoteCreateAt(t, "TEST-5", 3)
	require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{create}))
	out, err := retryQuarantinedEvents(ctx, testIngest(nil), app.store, 100)

	require.NoError(t, err)
	require.Equal(t, 1, out.applied)
	require.Empty(t, quarantined(t))
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, orphan.EventID))
}

func TestCloneCompleteLine_ReportsOrphanCount(t *testing.T) {
	require.Equal(t, "clone complete: 3 events applied across 1 batches\n", cloneCompleteLine(3, 1, 0))
	got := cloneCompleteLine(3, 1, 2)
	require.Contains(t, got, "2 orphaned events quarantined")
	require.Contains(t, got, "mtix sync quarantine list")
}

func TestFinishClone_WithQuarantinedOrphans_PrintsCount(t *testing.T) {
	initTestApp(t)
	_, orphan, _ := orphanHistory(t)
	require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, []*model.SyncEvent{orphan}))
	var stdout bytes.Buffer

	require.NoError(t, finishClone(context.Background(), &stdout, &bytes.Buffer{}, 4, 2))

	require.Equal(t, cloneCompleteLine(4, 2, 1), stdout.String())
	require.Contains(t, stdout.String(), "1 orphaned events quarantined")
}

func TestDoctorQuarantine_CloneOrphans_NamesThemAndFixAcknowledgesToGreen(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	_, orphan, _ := orphanHistory(t)
	require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{orphan}))

	ok, detail := checkQuarantinedEvents(ctx, app.store)
	require.False(t, ok)
	require.Contains(t, detail, "1 of them clone orphans")
	require.Contains(t, detail, "mtix sync quarantine acknowledge --handle")
	report := appendQuarantineCheck(ctx, DoctorReport{OverallPass: true}, app.store)
	require.Contains(t, report.Checks[0].Fix, "mtix sync quarantine acknowledge --handle "+service.QuarantineHandle(orphan.EventID))

	_, err := app.syncSvc.AcknowledgeQuarantine(ctx, orphan.EventID, "op-a", "task lives under its renumbered id")
	require.NoError(t, err)
	ok, detail = checkQuarantinedEvents(ctx, app.store)
	require.True(t, ok, detail)
	require.Len(t, quarantined(t), 1, "acknowledged events stay retained")
}
