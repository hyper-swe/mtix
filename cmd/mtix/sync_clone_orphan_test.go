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

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/clock"
	"github.com/hyper-swe/mtix/internal/sync/validator"
)

// Orphaned dependent events in a clone (MTIX-95.37.1): the hub holds events
// for a task the clone never creates (pushed under an old number by a client
// before 0.5.4). Driven through checkThenClone and applyBatch with fake pullers.

// orphanClaim is a claim of nodeID by a teammate, as a client before 0.5.4
// pushed it: no uid, so apply resolves it by node id.
func validatorMaxClock() int64 { return validator.MaxLamportClock }

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
	require.Equal(t, 2, pulled, "three events read, one quarantined: two applied")
	require.Equal(t, 1, batches)
	requireAppliedOnce(t, first)
	requireAppliedOnce(t, last)
	requireNotApplied(t, orphan)
	q := quarantined(t)[orphan.EventID]
	require.Equal(t, quarantineSourcePull, q.Source)
	require.Contains(t, q.Reason, sqlite.CloneOrphanReason, "the row records the clone origin")
	require.Len(t, quarantined(t), 1, "one row, not one per attempt")
	require.Contains(t, stderr.String(), format.EventID(orphan.EventID), "the clone names the quarantined event")
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

// failNthPullHub serves its events like fakeLateHub and fails PullEvents call
// number failCall once, as an interruption of the clone.
type failNthPullHub struct {
	fakeLateHub
	failCall, calls int
}

func (h *failNthPullHub) PullEvents(ctx context.Context, since transport.PullCursor, limit int) ([]*model.SyncEvent, bool, error) {
	h.calls++
	if h.calls == h.failCall {
		return nil, false, errors.New("hub connection lost")
	}
	return h.fakeLateHub.PullEvents(ctx, since, limit)
}

// TestCheckThenClone_ResumeAfterInterruption_KeepsOrphansOfCommittedBatches
// goes through the real resume: batch one (a create and an orphan claim)
// commits and saves the checkpoint, the fetch of batch two fails, and the
// resume reads the checkpoint and runs checkThenClone again. The resume never
// reads the orphan again and must not lose it: one row, the clone-origin
// marker, counted, acknowledgeable, and recorded exactly once.
func TestCheckThenClone_ResumeAfterInterruption_KeepsOrphansOfCommittedBatches(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	first := remoteCreateAt(t, "TEST-1", 1)
	orphan := orphanClaim(t, "TEST-9.1", 2)
	third := remoteCreateAt(t, "TEST-2", 3)
	fourth := remoteCreateAt(t, "TEST-3", 4)
	events := []*model.SyncEvent{first, orphan, third, fourth}
	// Calls 1-2 are the check, call 3 is batch one, call 4 (batch two) fails.
	hub := &failNthPullHub{fakeLateHub: fakeLateHub{pullEvents: events}, failCall: 4}

	_, _, stage, err := checkThenClone(ctx, &bytes.Buffer{}, hub, transport.PullCursor{}, 2)
	require.ErrorContains(t, err, "hub connection lost")
	require.Equal(t, "clone loop", stage)
	require.Len(t, quarantined(t), 1, "precondition: batch one committed its orphan")

	since, err := readCloneCheckpoint(ctx, app.store, true)
	require.NoError(t, err)
	resumeHub := &fakeLateHub{pullEvents: events}
	pulled, _, stage, err := checkThenClone(ctx, &bytes.Buffer{}, resumeHub, since, 2)

	require.NoError(t, err, stage)
	require.Equal(t, 2, pulled, "the resume reads only the two events after the checkpoint")
	rows := quarantined(t)
	require.Len(t, rows, 1)
	require.Contains(t, rows[orphan.EventID].Reason, sqlite.CloneOrphanReason)
	n, err := countCloneOrphans(ctx, app.store)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	requireAppliedOnce(t, third)
	requireAppliedOnce(t, fourth)
	_, err = app.syncSvc.AcknowledgeQuarantine(ctx, orphan.EventID, "op-a", "task lives under its renumbered id")
	require.NoError(t, err)
}

// TestApplyBatch_CreateUnderMissingParent_QuarantinedAsOrphan: a child create
// pushed under an old number names a parent the clone never creates; the
// insert fails its foreign key, not ErrNotFound, and is quarantined too.
func TestApplyBatch_CreateUnderMissingParent_QuarantinedAsOrphan(t *testing.T) {
	initTestApp(t)
	child := childCreate(t, "TEST-9.1", "TEST-9", 2)

	require.NoError(t, applyBatch(context.Background(), testIngest(nil), app.store, []*model.SyncEvent{child}))

	requireNotApplied(t, child)
	require.Contains(t, quarantined(t)[child.EventID].Reason, sqlite.CloneOrphanReason)
}

// TestApplyBatch_CreateFaultWithParentPresent_FailsBatch: a create whose
// parent exists and whose apply fails for another reason is no orphan.
func TestApplyBatch_CreateFaultWithParentPresent_FailsBatch(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	parent := remoteCreateAt(t, "TEST-9", 1)
	require.NoError(t, applyBatch(ctx, testIngest(nil), app.store, []*model.SyncEvent{parent}))
	child := childCreate(t, "TEST-9.1", "TEST-9", 2)
	in := testIngest(nil)
	in.apply = func(context.Context, *sql.Tx, *model.SyncEvent) error { return errors.New("disk exploded") }

	err := applyBatch(ctx, in, app.store, []*model.SyncEvent{child})

	require.ErrorContains(t, err, "disk exploded")
	require.Empty(t, quarantined(t))
}

func childCreate(t *testing.T, nodeID, parentID string, lamport int64) *model.SyncEvent {
	t.Helper()
	e := remoteCreateAt(t, nodeID, lamport)
	payload, err := model.EncodePayload(model.CreateNodePayload{
		Title: "child " + nodeID, ParentID: parentID, NodeType: model.NodeTypeForDepth(1),
		Priority: model.PriorityMedium, Creator: "remote-author",
	})
	require.NoError(t, err)
	e.Payload = payload
	return e
}

// TestCloneLoop_BatchRolledBack_ReportsNoQuarantine: the orphan lines are
// printed only after a batch commits; a batch that fails after quarantining
// an orphan rolls it back and announces nothing.
func TestCloneLoop_BatchRolledBack_ReportsNoQuarantine(t *testing.T) {
	initTestApp(t)
	orphan := orphanClaim(t, "TEST-9.1", 2)
	extreme := remoteCreateAt(t, "TEST-2", validatorMaxClock()-2)
	var stderr bytes.Buffer
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{orphan, extreme}}

	_, _, err := cloneLoop(context.Background(), &stderr, hub, app.store, transport.PullCursor{}, 10)

	require.ErrorIs(t, err, errCloneRefused)
	require.NotContains(t, stderr.String(), format.EventID(orphan.EventID))
	require.Empty(t, quarantined(t))
}

// TestCloneLoop_OrphansCommitted_ReportedAfterCommit names each quarantined
// event on stderr.
func TestCloneLoop_OrphanCommitted_NamedOnStderr(t *testing.T) {
	initTestApp(t)
	orphan := orphanClaim(t, "TEST-9.1", 2)
	var stderr bytes.Buffer
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{orphan}}

	pulled, _, err := cloneLoop(context.Background(), &stderr, hub, app.store, transport.PullCursor{}, 10)

	require.NoError(t, err)
	require.Zero(t, pulled, "a quarantined event is not counted as applied")
	require.Contains(t, stderr.String(), "quarantined event "+format.EventID(orphan.EventID))
}

// TestCloneLoop_OrphanWhoseCreateComesLater_AppliedByEndRetry: the claim
// precedes its task's creation in Lamport order, so it is quarantined when
// read; the end-of-clone retry applies it and it leaves the quarantine.
func TestCloneLoop_OrphanWhoseCreateComesLater_AppliedByEndRetry(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	claim := orphanClaim(t, "TEST-5", 1)
	create := remoteCreateAt(t, "TEST-5", 2)
	hub := &fakeLateHub{pullEvents: []*model.SyncEvent{claim, create}}

	pulled, _, err := cloneLoop(ctx, &bytes.Buffer{}, hub, app.store, transport.PullCursor{}, 1)

	require.NoError(t, err)
	require.Equal(t, 2, pulled, "the create, then the retried claim")
	require.Empty(t, quarantined(t))
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, claim.EventID))
}
