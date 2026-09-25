// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The subtree rule finds the task an event is about by its uid (MTIX-95.12,
// review r5 S1, S2 and S3). A local renumber moves a held creation's whole
// subtree to new numbers, and another task can then take an old number;
// events are held by the task they are about, not by the number they name.

// requireSent asserts the latest event of op for node reached hub.
func requireSent(t *testing.T, hub *fakePushHub, node string, op model.OpType) {
	t.Helper()
	id := eventIDFor(t, node, op)
	require.True(t, hub.accepted[id], "%s %s is pushed", node, op)
	require.Equal(t, "pushed", syncStatusOf(t, id))
}

// TestPushLoop_TwoLocalRenumbers_MiddleNumberChangesHeld is the review r5
// S1 probe: a held creation renumbered on this machine twice (TEST-1 to
// TEST-5 to TEST-7), with a push that fails midway in between. An edit, a
// comment, a child with its own edit, two edits and two links to the task,
// all made under the middle number TEST-5, stay held and are never sent:
// one edit and one link held by the failed push, the others made after it
// and first checked once TEST-5 is no longer the task (for the link, the
// task's own edit had named TEST-5 before it). An unrelated edit in the
// same pushes is sent.
func TestPushLoop_TwoLocalRenumbers_MiddleNumberChangesHeld(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))

	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-1", 5))
	require.NoError(t, runUpdate("TEST-5", "edit under the middle number", "", "", "", 0, "", ""))
	require.NoError(t, runComment("TEST-5", "comment under the middle number", ""))
	require.NoError(t, runCreate("child", "TEST-5", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-5.1", "child edit under the middle number", "", "", "", 0, "", ""))
	require.NoError(t, runDepAdd("TEST-2", "TEST-5", "related"))
	require.NoError(t, runUpdate("TEST-2", "unrelated edit", "", "", "", 0, "", ""))
	hub.failCalls = 1
	require.Error(t, pushWith(t, hub), "the push fails midway")
	linkHeld := eventIDFor(t, "TEST-2", model.OpLinkDep)
	editHeld := eventIDFor(t, "TEST-5", model.OpUpdateField)
	require.NoError(t, runDepAdd("TEST-2", "TEST-5", "blocks"))
	require.NoError(t, runUpdate("TEST-5", "second edit under the middle number", "", "", "", 0, "", ""))

	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-5", 7))
	require.NoError(t, pushWith(t, hub))
	child := eventIDFor(t, "TEST-5.1", model.OpCreateNode)
	requireHeldDependent(t, hub, "TEST-5", model.OpUpdateField, root)
	requireHeldDependent(t, hub, "TEST-5", model.OpComment, root)
	requireHeldDependent(t, hub, "TEST-5.1", model.OpCreateNode, root)
	requireHeldDependent(t, hub, "TEST-5.1", model.OpUpdateField, child)
	requireHeldDependent(t, hub, "TEST-2", model.OpLinkDep, root)
	for _, id := range []string{linkHeld, editHeld} {
		_, held := quarantined(t)[id]
		require.True(t, held, "what the failed push held stays held")
		require.False(t, hub.sent(id))
	}
	requireSent(t, hub, "TEST-2", model.OpUpdateField)
}

// TestPushLoop_TeammateTaskUnderOldNumber_Pushed is the review r5 S2
// probe: after a held creation is renumbered on this machine (TEST-1 to
// TEST-5), a teammate's TEST-1 is pulled in. A comment, a claim and a
// child on the teammate's task are pushed; the held task's own changes are
// not. Both links that name TEST-1, the one made when TEST-1 was the held
// task and the one made after the teammate's task took the number, wait
// for the held creation: links made while a creation is held wait for it,
// as a link names its target by number only.
func TestPushLoop_TeammateTaskUnderOldNumber_Pushed(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	require.NoError(t, runCreate("B's task", "", "", 3, "", "", "", "", ""))
	teammate := newFakePushHub()
	require.NoError(t, pushWith(t, teammate))

	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("A's held task", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("A's other task", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, runDepAdd("TEST-2", "TEST-1", "related"))
	linkBefore := eventIDFor(t, "TEST-2", model.OpLinkDep)
	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-1", 5))
	var stderr bytes.Buffer
	_, _, err := pullLoop(ctx, testIngest(&stderr), &fakeLateHub{pullEvents: teammate.events}, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	node, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B's task", node.Title)

	require.NoError(t, runDepAdd("TEST-2", "TEST-1", "related"))
	require.NoError(t, runComment("TEST-1", "comment on B's task", ""))
	require.NoError(t, runClaim("TEST-1", "agent-a"))
	require.NoError(t, runCreate("child of B's task", "TEST-1", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-5", "A's held task, retitled", "", "", "", 0, "", ""))
	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-2", model.OpLinkDep, root)
	requireSent(t, hub, "TEST-1", model.OpComment)
	requireSent(t, hub, "TEST-1", model.OpClaim)
	requireSent(t, hub, "TEST-1.1", model.OpCreateNode)
	requireHeldDependent(t, hub, "TEST-5", model.OpUpdateField, root)
	require.False(t, hub.sent(root), "the held creation stays held")
	q, ok := quarantined(t)[linkBefore]
	require.True(t, ok, "the link made when TEST-1 was the held task is held")
	require.True(t, strings.HasPrefix(q.Reason, holdDependsPrefix+root), q.Reason)
	require.False(t, hub.sent(linkBefore))
}

// TestPushLoop_HeldCreationRenumberedThenDeleted_DeleteHeld: a held
// creation renumbered on this machine and then deleted still blocks: the
// delete, made under its new number, is held (review r5 S2, M27). A
// soft-deleted task counts as the task an event is about.
func TestPushLoop_HeldCreationRenumberedThenDeleted_DeleteHeld(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, app.store.RenumberSubtree(context.Background(), "TEST-1", 5))
	require.NoError(t, runDelete("TEST-5", false))

	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-5", model.OpDelete, root)
	require.Empty(t, hub.calls, "nothing of the held task reaches the hub")
}

// TestPushLoop_ReleasedDependentInvalid_StaysHeldWhenPushFails is review r5
// S3: when a clock hold on a creation clears, a dependent below it that the
// hub would refuse (a child creation over the size limit) is validated
// before it is released. It stays held under its own reason, with its
// first_seen kept and one more attempt, and it keeps blocking its own
// subtree. The push then fails on its first batch, before the child's
// batch is read: status and doctor still count the child and its edit as
// held.
func TestPushLoop_ReleasedDependentInvalid_StaysHeldWhenPushFails(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, runCreate("ahead", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	stampInFuture(t, root)
	require.NoError(t, runDecomposeChildren("TEST-1", childInputs(pushBatchSize, "sibling")))
	require.NoError(t, runCreate("oversized child", "TEST-1", "", 3, "", overWireCap(), "", "", ""))
	big := fmt.Sprintf("TEST-1.%d", pushBatchSize+1)
	require.NoError(t, runUpdate(big, "oversized child, retitled", "", "", "", 0, "", ""))
	child := eventIDFor(t, big, model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	before := quarantined(t)[child]
	require.True(t, strings.HasPrefix(before.Reason, holdDependsPrefix), before.Reason)

	stampAt(t, root, time.Now())
	hub.failCalls = 1
	require.Error(t, pushWith(t, hub), "the first batch fails")
	after := quarantined(t)[child]
	require.True(t, strings.HasPrefix(after.Reason, holdTooLargePrefix), after.Reason)
	require.Equal(t, before.FirstSeen, after.FirstSeen, "first_seen is kept")
	require.Equal(t, before.Attempts+1, after.Attempts)
	requireHeldDependent(t, hub, big, model.OpUpdateField, child)
	require.Equal(t, 2, pushHoldCount(t))

	var out, errOut bytes.Buffer
	app.jsonOutput = true
	require.NoError(t, runSyncStatus(ctx, &out, &errOut))
	var status map[string]any
	require.NoError(t, json.Unmarshal(out.Bytes(), &status))
	require.Equal(t, float64(2), status["held_push_events"])
	out.Reset()
	require.ErrorIs(t, runSyncDoctor(ctx, &out, &errOut, nil, transport.Options{}), errDoctorChecksFailed)
	check := doctorCheckNamed(t, out.Bytes(), "held push events")
	require.False(t, check.Pass)
	require.Contains(t, check.Detail, "2 push events held")
	require.Contains(t, check.Detail, big+" create_node: stop editing this task and escalate")
}

// childInputs returns n decompose inputs titled "<tag> <i>".
func childInputs(n int, tag string) []service.DecomposeInput {
	out := make([]service.DecomposeInput, n)
	for i := range out {
		out[i] = service.DecomposeInput{Title: fmt.Sprintf("%s %d", tag, i+1)}
	}
	return out
}

// TestPushLoop_EventWithoutUID_HeldByNumber: an event without a uid (made
// for a task that had none, before uids existed) cannot be matched to its
// task, so it falls back to the numbers it names, checked against the
// number the held creation's event names and the task's current number: an
// edit naming TEST-1 before a local renumber, and an edit and a link naming
// TEST-5 after it, are held; an edit naming an unrelated task is pushed.
func TestPushLoop_EventWithoutUID_HeldByNumber(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, runUpdate("TEST-1", "edit without a uid", "", "", "", 0, "", ""))
	require.NoError(t, app.store.RenumberSubtree(context.Background(), "TEST-1", 5))
	require.NoError(t, runUpdate("TEST-5", "edit without a uid, renumbered", "", "", "", 0, "", ""))
	require.NoError(t, runDepAdd("TEST-2", "TEST-5", "related"))
	require.NoError(t, runUpdate("TEST-2", "unrelated edit without a uid", "", "", "", 0, "", ""))
	for _, e := range []struct {
		node string
		op   model.OpType
	}{{"TEST-1", model.OpUpdateField}, {"TEST-5", model.OpUpdateField}, {"TEST-2", model.OpLinkDep},
		{"TEST-2", model.OpUpdateField}} {
		_, err := app.store.WriteDB().ExecContext(context.Background(),
			`UPDATE sync_events SET uid = NULL WHERE event_id = ?`, eventIDFor(t, e.node, e.op))
		require.NoError(t, err)
	}

	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-1", model.OpUpdateField, root)
	requireHeldDependent(t, hub, "TEST-5", model.OpUpdateField, root)
	requireHeldDependent(t, hub, "TEST-2", model.OpLinkDep, root)
	requireSent(t, hub, "TEST-2", model.OpUpdateField)
}

// TestPushLoop_LinkHeldForRenumberedTask_StaysHeld: a link made to a held
// task right after a local renumber (TEST-1 to TEST-5), with no other
// change of the task naming TEST-5, is held while TEST-5 is the task. After
// a second renumber (TEST-5 to TEST-7) nothing on this machine records
// that TEST-5 was the task any more, and the link stays held because the
// creation its reason names is still held.
func TestPushLoop_LinkHeldForRenumberedTask_StaysHeld(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	require.NoError(t, runCreate("held", "", "", 3, "", overWireCap(), "", "", ""))
	require.NoError(t, runCreate("other", "", "", 3, "", "", "", "", ""))
	root := eventIDFor(t, "TEST-1", model.OpCreateNode)
	hub := newFakePushHub()
	require.NoError(t, pushWith(t, hub))
	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-1", 5))
	require.NoError(t, runDepAdd("TEST-2", "TEST-5", "related"))
	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-2", model.OpLinkDep, root)

	require.NoError(t, app.store.RenumberSubtree(ctx, "TEST-5", 7))
	require.NoError(t, pushWith(t, hub))
	requireHeldDependent(t, hub, "TEST-2", model.OpLinkDep, root)
}
