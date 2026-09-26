// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// The subtree rule of held task creations (MTIX-95.12). While a task's
// creation is held, every event of that task and its whole subtree is held,
// with a reason naming the nearest held creation. When a clock hold clears,
// the creation and its whole subtree are released together and go through
// the push as ordinary pending events. A size-held creation's subtree stays
// held.

// pushWith runs one push against hub and returns its error.
func pushWith(t *testing.T, hub *fakePushHub) error {
	t.Helper()
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(context.Background(), &stderr, hub, app.store)
	return err
}

// pushHoldCount returns how many push holds the store has.
func pushHoldCount(t *testing.T) int {
	t.Helper()
	return countTestRows(t, `SELECT COUNT(*) FROM sync_quarantine WHERE source = 'push'`)
}

// TestPushLoop_HeldCreationSubtree_InBatchGrandchildNamesNearest: a child,
// a grandchild and an edit of the grandchild, made in the same batch as
// their held ancestor's creation, are all held, each naming its nearest
// held creation.
func TestPushLoop_HeldCreationSubtree_InBatchGrandchildNamesNearest(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("grandchild", "TEST-1.1", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1.1.1", "grandchild edit", "", "", "", 0, "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	child := eventIDFor(t, "TEST-1.1", model.OpCreateNode)
	grandchild := eventIDFor(t, "TEST-1.1.1", model.OpCreateNode)

	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-1.1", model.OpCreateNode, root)
	requireHeldDependent(t, hub, "TEST-1.1.1", model.OpCreateNode, child)
	requireHeldDependent(t, hub, "TEST-1.1.1", model.OpUpdateField, grandchild)
}

// TestPushLoop_EditWhileCreationHeld_Held: an edit made while the task's
// creation is held, after the last push and with no push in between, is
// held too and not sent.
func TestPushLoop_EditWhileCreationHeld_Held(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("ahead", "", "", 3, "", "", "", "", ""))
	create := eventIDFor(t, "TEST-1", model.OpCreateNode)
	stampInFuture(t, create)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, runUpdate("TEST-1", "edit made while held", "", "", "", 0, "", ""))

	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-1", model.OpUpdateField, create)
}

// TestPushLoop_ClockHoldClears_CreationAndSubtreeInOnePush: once a clock
// hold on a creation clears, the creation and its full subtree (edits made
// before and after the last push, a claim, a child with its own edit, and a
// link that names the task) go out in one push, the holds drain to zero,
// and a later push sends nothing twice.
func TestPushLoop_ClockHoldClears_CreationAndSubtreeInOnePush(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("ahead", "", "", 3, "", "", "", "", ""))
	create := eventIDFor(t, "TEST-2", model.OpCreateNode)
	stampInFuture(t, create)
	require.NoError(t, runUpdate("TEST-2", "edit before the push", "", "", "", 0, "", ""))
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, runUpdate("TEST-2", "edit after the push", "", "", "", 0, "", ""))
	require.NoError(t, runClaim("TEST-2", "agent-a"))
	require.NoError(t, runCreate("child", "TEST-2", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-2.1", "child edit", "", "", "", 0, "", ""))
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "related"))
	require.NoError(t, pushWith(t, hub))
	var subtree []string
	for _, id := range []string{eventIDFor(t, "TEST-2", model.OpClaim), eventIDFor(t, "TEST-2.1", model.OpCreateNode),
		eventIDFor(t, "TEST-2.1", model.OpUpdateField), eventIDFor(t, "TEST-1", model.OpLinkDep)} {
		require.False(t, hub.sent(id), "nothing of the subtree is sent while the creation is held")
		subtree = append(subtree, id)
	}
	stampAt(t, create, time.Now())

	before := len(hub.calls)
	require.NoError(t, pushWith(t, hub))
	pushed := map[string]bool{}
	for _, call := range hub.calls[before:] {
		for _, id := range call {
			pushed[id] = true
		}
	}
	require.True(t, pushed[create], "the released creation is pushed")
	for _, id := range subtree {
		require.True(t, pushed[id] && hub.accepted[id], "its subtree goes out in the same push")
	}
	require.Zero(t, pushHoldCount(t), "the holds drain to zero")
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`))

	sent := len(hub.events)
	require.NoError(t, pushWith(t, hub))
	require.Len(t, hub.events, sent, "a later push sends nothing twice")
}

// TestPushLoop_SizeHeldCreation_SubtreeHeldAcrossPushes: a creation held for
// its size never releases its subtree: across repeated pushes nothing of it
// is sent, the number of holds stays the same, and each push counts one
// attempt for every dependent it checked.
func TestPushLoop_SizeHeldCreation_SubtreeHeldAcrossPushes(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runUpdate("TEST-1", "edit", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	edit := eventIDFor(t, "TEST-1", model.OpUpdateField)
	child := eventIDFor(t, "TEST-1.1", model.OpCreateNode)
	hub := newFakePushHub()
	for push := 1; push <= 3; push++ {
		require.NoError(t, pushWith(t, hub))
		require.Equal(t, 3, pushHoldCount(t), "push %d: no hold leaks or grows", push)
		held := quarantined(t)
		require.Equal(t, 1, held[root].Attempts, "a permanent hold is not checked again")
		require.Equal(t, push, held[edit].Attempts, "push %d counts the dependent it checked", push)
		require.Equal(t, push, held[child].Attempts)
	}
	require.Empty(t, hub.calls, "nothing of the held subtree reaches the hub")
}

// TestPushLoop_LinkToSizeHeldTask_WaitsAfterClockClears: a link made while
// a clock-held creation and a later size-held one are held waits for the
// earliest, the clock-held one; when that clock hold clears, the link still
// waits for the size-held creation, relabeled with it, one more attempt
// counted.
func TestPushLoop_LinkToSizeHeldTask_WaitsAfterClockClears(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("ahead", "", "", 3, "", "", "", "", ""))
	clocked := eventIDFor(t, "TEST-1", model.OpCreateNode)
	stampInFuture(t, clocked)
	require.NoError(t, runCreate("held for size", "", "", 3, "", overWireCap(), "", "", ""))
	sized := eventIDFor(t, "TEST-2", model.OpCreateNode)
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "related"))
	link := eventIDFor(t, "TEST-1", model.OpLinkDep)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-1", model.OpLinkDep, clocked)
	require.Contains(t, quarantined(t)[link].Reason, holdLinkWait)
	stampAt(t, clocked, time.Now())

	require.NoError(t, pushWith(t, hub))
	require.True(t, hub.accepted[clocked], "the clock-held creation is released and pushed")
	requireHeldDependent(t, hub, "TEST-1", model.OpLinkDep, sized)
	require.Contains(t, quarantined(t)[link].Reason, holdLinkWait)
	require.Equal(t, 2, quarantined(t)[link].Attempts)
}

// TestPushLoop_RenumberedCreate_SentAgainInSamePush: a creation the hub
// renumbers keeps its Lamport clock when it is re-queued, behind the events
// already read, so the push reads the queue from its start again and sends
// the creation under its new number in the same push.
func TestPushLoop_RenumberedCreate_SentAgainInSamePush(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("first", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("second", "", "", 3, "", "", "", "", ""))
	create := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	hub.renumberOnce = map[string]bool{create: true}

	require.NoError(t, pushWith(t, hub))
	require.True(t, hub.accepted[create], "the renumbered creation is sent again in the same push")
	require.Equal(t, "TEST-3", hub.events[len(hub.events)-1].NodeID)
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`))
}

// TestPushLoop_HeldCreationRenumberedLocally_NewNumberHeld: a task whose
// held creation names its old number, renumbered on this machine without a
// sync event (the store renumber an import merge or a settle uses), still
// blocks its subtree: a change and a child made under its new number are
// held, naming the creation, whether the creation was first held before
// the renumber or in the same push as those changes.
func TestPushLoop_HeldCreationRenumberedLocally_NewNumberHeld(t *testing.T) {
	for _, heldFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("creation held before the renumber: %v", heldFirst), func(t *testing.T) {
			initTestApp(t)
			require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
			create := eventIDFor(t, "TEST-1", model.OpCreateNode)
			hub := newFakePushHub()
			if heldFirst {
				require.NoError(t, pushWith(t, hub))
			}
			require.NoError(t, app.store.RenumberSubtree(context.Background(), "TEST-1", 5))
			require.NoError(t, runUpdate("TEST-5", "edit under the new number", "", "", "", 0, "", ""))
			require.NoError(t, runCreate("child", "TEST-5", "", 3, "", "", "", "", ""))

			require.NoError(t, pushWith(t, hub))
			requireHeldDependent(t, hub, "TEST-5", model.OpUpdateField, create)
			requireHeldDependent(t, hub, "TEST-5.1", model.OpCreateNode, create)
			require.Empty(t, hub.calls, "nothing of the renumbered task reaches the hub")
		})
	}
}
