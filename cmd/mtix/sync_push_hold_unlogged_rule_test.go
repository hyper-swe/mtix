// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Which unlogged task a hold names, and how an unlogged hold meets the
// subtree rule inside one batch (MTIX-112.17.16.37, review r1 F4 and F8).

// TestPushLoop_UnloggedParentAndChild_ReasonAndDoctorNameTheParent: TEST-1
// and its child TEST-1.1 both have no logged creation; an edit of the child
// is held with a reason naming the topmost unlogged task, TEST-1, and
// doctor names TEST-1 with its one held event and gives the fix for TEST-1,
// not for the child.
func TestPushLoop_UnloggedParentAndChild_ReasonAndDoctorNameTheParent(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("parent", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("child", "TEST-1", "", 3, "", "", "", "", ""))
	unlogTask(t, "TEST-1")
	unlogTask(t, "TEST-1.1")
	require.NoError(t, runUpdate("TEST-1.1", "child retitled", "", "", "", 0, "", ""))
	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, newFakePushHub(), app.store)
	require.NoError(t, err, stderr.String())

	edit := eventIDFor(t, "TEST-1.1", model.OpUpdateField)
	reason, held := unloggedHolds(t)[edit]
	require.True(t, held, stderr.String())
	require.Contains(t, reason, "task TEST-1 (uid ", "the reason names the topmost unlogged task")
	require.NotContains(t, reason, "task TEST-1.1 ")

	c := findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store), unloggedCheckName)
	require.False(t, c.Pass)
	require.Contains(t, c.Detail, ": TEST-1 (1 held events)")
	require.NotContains(t, c.Detail, "TEST-1.1 (")
	require.Contains(t, c.Fix, "mtix delete TEST-1 --cascade")
	require.NotContains(t, c.Fix, "TEST-1.1")
}

// TestPushLoop_UnloggedChildCreation_DoesNotMakeALaterLinkInItsBatchWait:
// one batch holds, in queue order, the creation of a child under an
// unlogged task, a link between two logged tasks and a creation stamped
// two days ahead. The clock-refused creation makes push read the batch's
// tasks and apply the subtree rule; the unlogged-held child creation does
// not join the held creations there, so the link, made before the
// clock-held creation, is pushed rather than held as a link that waits.
func TestPushLoop_UnloggedChildCreation_DoesNotMakeALaterLinkInItsBatchWait(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("logged", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("imported", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("logged too", "", "", 3, "", "", "", "", ""))
	unlogTask(t, "TEST-2")
	hub := newFakePushHub()
	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())

	require.NoError(t, runCreate("child", "TEST-2", "", 3, "", "", "", "", ""))
	require.NoError(t, runDepAdd("TEST-1", "TEST-3", "blocks"))
	require.NoError(t, runCreate("stamped ahead", "", "", 3, "", "", "", "", ""))
	future := eventIDFor(t, "TEST-4", model.OpCreateNode)
	stampInFuture(t, future)
	_, err = pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())

	q := quarantined(t)
	require.True(t, strings.HasPrefix(q[future].Reason, holdClockPrefix), "the creation stamped ahead is clock-held")
	require.Contains(t, unloggedHolds(t), eventIDFor(t, "TEST-2.1", model.OpCreateNode))
	link := eventIDFor(t, "TEST-1", model.OpLinkDep)
	require.True(t, hub.sent(link), "the link is not held behind the unlogged child creation: %s", q[link].Reason)
	require.Equal(t, "pushed", syncStatusOf(t, link))
}
