// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Held creations whose tasks move while held, and links made while a
// creation is held (MTIX-95.12, pre-review of round 5). A renumber the hub
// asks for in the middle of a push, or one committed by another process
// while the push runs, moves a subtree; push reads its held creations again
// before the next batch. A link names its target by number only, so while
// a creation is held, every link or unlink made after it waits for it. A
// task purged by mtix gc has no node left, so its events are checked by
// the numbers they name.

// otherEdits makes n title edits of node, pending events that fill batches.
func otherEdits(t *testing.T, node string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		require.NoError(t, runUpdate(node, "other edit "+strings.Repeat("x", i%7), "", "", "", 0, "", ""))
	}
}

// TestPushLoop_HubRenumbersAncestorMidPush_HeldChildEditStaysHeld is the
// pre-review S1 probe: the hub asks for a renumber of TEST-1, which moves
// its held child TEST-1.1 in the middle of the push. An edit of the child
// and the creation of a grandchild under it, read in a later batch of the
// same push after 100 other edits, stay held under the child's creation
// and are never sent. The grandchild is found only through the child's new
// number, so the held creations must be read again after the renumber.
func TestPushLoop_HubRenumbersAncestorMidPush_HeldChildEditStaysHeld(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("parent", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("held child", "TEST-1", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	otherEdits(t, "TEST-2", pushBatchSize)
	require.NoError(t, runUpdate("TEST-1.1", "child edit", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("grandchild", "TEST-1.1", "", 3, "", "", "", "", ""))
	parent := eventIDFor(t, "TEST-1", model.OpCreateNode)
	child := eventIDFor(t, "TEST-1.1", model.OpCreateNode)
	edit := eventIDFor(t, "TEST-1.1", model.OpUpdateField)
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{parent: true}

	require.NoError(t, pushWith(t, hub))
	require.True(t, hub.accepted[parent], "the renumbered parent is pushed")
	node, err := app.store.GetNode(context.Background(), "TEST-3.1")
	require.NoError(t, err)
	require.Equal(t, "child edit", node.Title, "the child moved with its parent")
	requireHeldDependent(t, hub, "TEST-1.1", model.OpUpdateField, child)
	requireHeldDependent(t, hub, "TEST-1.1.1", model.OpCreateNode, child)
	require.False(t, hub.sent(edit))
}

// TestPushLoop_RenumberByAnotherProcessMidPush_IndexReadAgain: a renumber
// of a held task committed by another connection while the push runs is
// seen before the next batch: an edit of the task and the creation of a
// child under it, read after the renumber, stay held. The child is found
// only through the task's new number.
func TestPushLoop_RenumberByAnotherProcessMidPush_IndexReadAgain(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	otherEdits(t, "TEST-2", pushBatchSize)
	require.NoError(t, runUpdate("TEST-1", "edit of the held task", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	hub.beforeCall = func(call int) {
		if call != 2 {
			return
		}
		other, err := sqlite.New(filepath.Join(app.mtixDir, "data"), app.logger)
		require.NoError(t, err)
		require.NoError(t, other.RenumberSubtree(context.Background(), "TEST-1", 5))
		require.NoError(t, other.Close())
	}

	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-1", model.OpUpdateField, root)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpCreateNode, root)
}

// TestPushLoop_LinkMadeBeforeHeldCreation_Pushed: a link made before a
// creation that push then holds does not wait for it. A failed push leaves
// the link pending after the creation is held, and the next push, which
// reads the held creation with its place in the queue, sends the link.
func TestPushLoop_LinkMadeBeforeHeldCreation_Pushed(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("one", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("two", "", "", 3, "", "", "", "", ""))
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "related"))
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	hub.failCalls = 1
	require.Error(t, pushWith(t, hub))
	require.Equal(t, 1, pushHoldCount(t), "the creation is held; the link is not")

	require.NoError(t, pushWith(t, hub))
	requireSent(t, hub, "TEST-1", model.OpLinkDep)
}

// TestPushLoop_BlocksLinkAcrossLocalRenumbers_Held is pre-review probe 2a:
// a held child renumbered on this machine twice (TEST-1.1 to TEST-1.5 to
// TEST-1.7), with a blocks link to it made under TEST-1.5 in between and no
// push or other change of the child. The link waits for the held creation.
func TestPushLoop_BlocksLinkAcrossLocalRenumbers_Held(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, runCreate("parent", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("held child", "TEST-1", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	child := eventIDFor(t, "TEST-1.1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-1.1", 5))
	require.NoError(t, runDepAdd("TEST-2", "TEST-1.5", "blocks"))
	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-1.5", 7))

	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-2", model.OpLinkDep, child)
	require.Contains(t, quarantined(t)[eventIDFor(t, "TEST-2", model.OpLinkDep)].Reason, holdLinkWait)
}

// TestPushLoop_LinkAfterHubRenumberAndFailedPush_Held is pre-review probe
// 2b: the hub renumbers a held child's parent (TEST-1 to TEST-3), a push
// fails before it reaches a blocks link made to the child under TEST-3.1
// after 100 other edits, and the parent is then renumbered on this machine
// (TEST-3 to TEST-7). The link waits for the held creation.
func TestPushLoop_LinkAfterHubRenumberAndFailedPush_Held(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("parent", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("held child", "TEST-1", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	parent := eventIDFor(t, "TEST-1", model.OpCreateNode)
	child := eventIDFor(t, "TEST-1.1", model.OpCreateNode)
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{parent: true}
	require.NoError(t, pushWith(t, hub))
	otherEdits(t, "TEST-2", pushBatchSize)
	require.NoError(t, runDepAdd("TEST-2", "TEST-3.1", "blocks"))
	link := eventIDFor(t, "TEST-2", model.OpLinkDep)
	hub.failCalls = 1
	require.Error(t, pushWith(t, hub), "the push fails before the link's batch")
	require.NoError(t, app.store.RenumberSubtree(context.Background(), "TEST-3", 7))

	require.NoError(t, pushWith(t, hub))
	require.False(t, hub.sent(link))
	requireHeldDependent(t, hub, "TEST-2", model.OpLinkDep, child)
}

// TestPushLoop_HeldTaskPurgedByGC_NothingSent is pre-review probe 3: a
// size-held task and its child, an edit of the child, then a cascade delete
// of the task, purged by mtix gc (retention 1ns). No node has their uids
// any more, so their events are checked by the numbers they name; nothing
// of them is sent, the delete included.
func TestPushLoop_HeldTaskPurgedByGC_NothingSent(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1.1", "child edit", "", "", "", 0, "", ""))
	require.NoError(t, runDelete("TEST-1", true))
	_, err := app.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET deleted_at = '2000-01-01T00:00:00Z' WHERE id IN ('TEST-1', 'TEST-1.1')`)
	require.NoError(t, err)
	require.NoError(t, runConfigSet("data.soft_delete_retention", "1ns"))
	require.NoError(t, runGC())
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE id LIKE 'TEST-1%'`), "gc purged the tasks")

	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.Empty(t, hub.calls, "nothing of the purged task reaches the hub")
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pushed'`))
	for _, e := range []struct {
		node string
		op   model.OpType
	}{{"TEST-1", model.OpCreateNode}, {"TEST-1.1", model.OpCreateNode}, {"TEST-1.1", model.OpUpdateField},
		{"TEST-1", model.OpDelete}} {
		_, held := quarantined(t)[eventIDFor(t, e.node, e.op)]
		require.True(t, held, "%s %s is held", e.node, e.op)
	}
}

// TestPushLoop_HeldTaskRenumberedThenPurged_NothingSent: a size-held task
// renumbered on this machine (TEST-1 to TEST-5), with an edit and a child
// made under TEST-5 and held by a push, then deleted and purged by mtix gc.
// No node is left for the rule to read, and TEST-5 is neither the number
// the held creation's event names nor a current one, but the held events
// stay held while the creation they were held for is (heldFor), and the
// delete is of the held creation's own task, found by uid: nothing of them
// is sent.
func TestPushLoop_HeldTaskRenumberedThenPurged_NothingSent(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-1", 5))
	require.NoError(t, runUpdate("TEST-5", "edit under TEST-5", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("child", "TEST-5", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-5.1", "child edit", "", "", "", 0, "", ""))
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.Equal(t, 4, pushHoldCount(t))
	require.NoError(t, runDelete("TEST-5", true))
	_, err := app.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET deleted_at = '2000-01-01T00:00:00Z' WHERE id IN ('TEST-5', 'TEST-5.1')`)
	require.NoError(t, err)
	require.NoError(t, runConfigSet("data.soft_delete_retention", "1ns"))
	require.NoError(t, runGC())
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE id LIKE 'TEST-5%'`), "gc purged the tasks")

	require.NoError(t, pushWith(t, hub))
	require.Empty(t, hub.calls, "nothing of the purged task reaches the hub")
	require.Equal(t, 5, pushHoldCount(t), "creation, edit, child creation, child edit and delete are held")
}

// TestPushLoop_RenumberedAndPurgedBeforeAnyPush_OwnChangesHeld: a size-held
// task renumbered (TEST-1 to TEST-5), edited under TEST-5, deleted and
// purged by mtix gc before any push checked it. In that push its creation
// is held first, and the edit and the delete, made under a number no held
// creation's event names, are still held: they are of the held creation's
// own task, found by uid.
func TestPushLoop_RenumberedAndPurgedBeforeAnyPush_OwnChangesHeld(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-1", 5))
	require.NoError(t, runUpdate("TEST-5", "edit under TEST-5", "", "", "", 0, "", ""))
	require.NoError(t, runDelete("TEST-5", false))
	_, err := app.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET deleted_at = '2000-01-01T00:00:00Z' WHERE id = 'TEST-5'`)
	require.NoError(t, err)
	require.NoError(t, runConfigSet("data.soft_delete_retention", "1ns"))
	require.NoError(t, runGC())
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE id = 'TEST-5'`), "gc purged the task")

	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-5", model.OpUpdateField, root)
	requireHeldDependent(t, hub, "TEST-5", model.OpDelete, root)
	require.Empty(t, hub.calls)
}

// renumberWhileLocked starts, on another connection to this project's
// database, a write transaction (BEGIN IMMEDIATE) that renumbers TEST-1 to
// TEST-5 with its subtree, and commits it after delay. It returns a channel
// that yields the transaction's error once it has committed.
func renumberWhileLocked(t *testing.T, delay time.Duration) <-chan error {
	t.Helper()
	ctx := context.Background()
	other, err := sqlite.New(filepath.Join(app.mtixDir, "data"), app.logger)
	require.NoError(t, err)
	tx, err := other.WriteDB().BeginTx(ctx, nil)
	require.NoError(t, err)
	for _, stmt := range []string{
		`PRAGMA defer_foreign_keys = ON`,
		`UPDATE nodes SET id = 'TEST-5' || SUBSTR(id, 7), parent_id = 'TEST-5' || SUBSTR(parent_id, 7)
		 WHERE id LIKE 'TEST-1.%'`,
		`UPDATE nodes SET id = 'TEST-5', seq = 5 WHERE id = 'TEST-1'`,
	} {
		_, err := tx.ExecContext(ctx, stmt)
		require.NoError(t, err)
	}
	done := make(chan error, 1)
	go func() {
		time.Sleep(delay)
		err := tx.Commit()
		if closeErr := other.Close(); err == nil {
			err = closeErr
		}
		done <- err
	}()
	return done
}

// TestPushLoop_RenumberCommittedWhileReleaseWaits_ChildHeld is pre-review 2
// S1: TEST-1 is size-held by a first push, then its child TEST-1.1 is
// created and edited. Another process holds the write lock with an
// uncommitted renumber of TEST-1 to TEST-5 and commits it 700 ms after the
// second push starts, while that push's release step waits for the lock.
// The push notes the data version before it reads its held creations, so
// it sees the renumber: the child's creation and edit stay held.
func TestPushLoop_RenumberCommittedWhileReleaseWaits_ChildHeld(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1.1", "child edit", "", "", "", 0, "", ""))
	done := renumberWhileLocked(t, 700*time.Millisecond)

	require.NoError(t, pushWith(t, hub))
	require.NoError(t, <-done)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpCreateNode, root)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpUpdateField, eventIDFor(t, "TEST-1.1", model.OpCreateNode))
	require.Empty(t, hub.calls)
}

// TestPushLoop_RenumberCommittedBeforeBatchTasksRead_BatchDecidedAgain: a
// renumber of a held task (TEST-1 to TEST-5) committed by another
// connection in the second batch, just before that batch's tasks are read,
// is seen after the batch is decided: push reads its held creations again
// and decides the batch again, so the creation and edit of a child of the
// held task in that batch stay held.
func TestPushLoop_RenumberCommittedBeforeBatchTasksRead_BatchDecidedAgain(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	otherEdits(t, "TEST-2", pushBatchSize)
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1.1", "child edit", "", "", "", 0, "", ""))
	calls := 0
	ctx := context.WithValue(context.Background(), pushSubjectsHookKey{}, func() {
		calls++
		if calls != 2 {
			return
		}
		other, err := sqlite.New(filepath.Join(app.mtixDir, "data"), app.logger)
		require.NoError(t, err)
		require.NoError(t, other.RenumberSubtree(context.Background(), "TEST-1", 5))
		require.NoError(t, other.Close())
	})

	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpCreateNode, root)
	requireHeldDependent(t, hub, "TEST-1.1", model.OpUpdateField, eventIDFor(t, "TEST-1.1", model.OpCreateNode))
	require.GreaterOrEqual(t, calls, 3, "the second batch's tasks are read again")
}
