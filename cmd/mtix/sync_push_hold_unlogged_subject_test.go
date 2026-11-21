// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// The subject of a retired hold of a task the sync log had no creation for
// (MTIX-112.17.16.37, lead ruling r2 2): every task a hold names, whichever
// held event put it there (its own events, a held child creation or any held
// event of a task below it, a held link to or from it). A delete of that
// task made before its creation is logged stays local, even when no event of
// the task itself was ever held.

// TestPushLoop_UnloggedTaskNamedOnlyThroughChildOrLink_FixThenPull_DeleteNeverSent
// is review r1d S1 against a fake hub: the only hold that names B's
// merge-imported TEST-1 is a child creation under it, or a link to or from
// it. A follows doctor's fix (re-create, then delete TEST-1 with its
// subtree) and pulls B's creation of TEST-1 before the next push. No push
// sends the held event or any delete of TEST-1's subtree; doctor passes with
// the retired note; a fresh peer holds B's TEST-1 live and quarantines
// nothing.
func TestPushLoop_UnloggedTaskNamedOnlyThroughChildOrLink_FixThenPull_DeleteNeverSent(t *testing.T) {
	tests := []struct {
		name string
		// hold makes the one event push holds and returns its id.
		hold func(t *testing.T) string
	}{
		{"child creation under it", func(t *testing.T) string {
			require.NoError(t, runCreate("child on A", "TEST-1", "", 3, "", "", "", "", ""))
			return eventIDFor(t, "TEST-1.1", model.OpCreateNode)
		}},
		{"link to it from a logged task", func(t *testing.T) string {
			require.NoError(t, runDepAdd("TEST-2", "TEST-1", "related"))
			return eventIDFor(t, "TEST-2", model.OpLinkDep)
		}},
		{"link from it to a logged task", func(t *testing.T) string {
			require.NoError(t, runDepAdd("TEST-1", "TEST-2", "related"))
			return eventIDFor(t, "TEST-1", model.OpLinkDep)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			var stderr bytes.Buffer
			hub := newFakePushHub()
			push := func() {
				_, err := pushLoop(ctx, &stderr, hub, app.store)
				require.NoError(t, err, stderr.String())
			}
			importTeammateBoard(t, func() error { push(); return nil })
			require.NoError(t, runCreate("logged on A", "", "", 3, "", "", "", "", ""))
			push()
			require.True(t, hub.sent(eventIDFor(t, "TEST-2", model.OpCreateNode)), stderr.String())
			held := tt.hold(t)
			push()
			holds := unloggedHolds(t)
			require.Len(t, holds, 1, "the one hold names TEST-1: %v", holds)
			require.Contains(t, holds[held], "task TEST-1 (uid ")

			require.NoError(t, runCreate("shared, re-created on A", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runDelete("TEST-1", true))
			local := append(ownEventsOf(t, "TEST-1"), held)
			if tt.name == "child creation under it" {
				local = append(local, ownEventsOf(t, "TEST-1.1")...)
			}
			pullFromHub(t, &stderr, hub)
			push()
			push()

			for _, id := range local {
				require.False(t, hub.sent(id), "A's event %s of TEST-1's subtree never reaches the hub: %s", id, stderr.String())
				require.Contains(t, unloggedHolds(t), id, "it stays held as a record")
			}
			require.Contains(t, unloggedHolds(t)[eventIDFor(t, "TEST-1", model.OpDelete)],
				"so this delete is never sent")
			require.True(t, hub.sent(eventIDFor(t, "TEST-3", model.OpCreateNode)), "the re-created task is sent")
			require.NotContains(t, stderr.String(), "released held event")
			requireRetiredNote(t)

			require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
			n, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err, "the teammate's task stays live on the peer")
			require.Nil(t, n.DeletedAt)
			require.Equal(t, "shared", n.Title)
			require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM dependencies`), "no held link reached the peer")
			require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE id = 'TEST-1.1'`))
		})
	}
}

// TestPushLoop_UnloggedHold_DeleteUndeleteThenDeleteAfterPull_LaterDeleteSent
// is review r1d X1: A edits B's merge-imported TEST-1 and deletes it (both
// held), undeletes it, pulls B's creation and deletes TEST-1 again. The rule
// reads the latest delete: the second delete, made after the creation is
// logged, is an ordinary delete and is sent after the held edit, the first
// one never; A and a fresh peer both hold TEST-1 deleted.
func TestPushLoop_UnloggedHold_DeleteUndeleteThenDeleteAfterPull_LaterDeleteSent(t *testing.T) {
	ctx := context.Background()
	var stderr bytes.Buffer
	hub := newFakePushHub()
	push := func() {
		_, err := pushLoop(ctx, &stderr, hub, app.store)
		require.NoError(t, err, stderr.String())
	}
	importTeammateBoard(t, func() error { push(); return nil })
	require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
	require.NoError(t, runDelete("TEST-1", true))
	push()
	edit := eventIDFor(t, "TEST-1", model.OpUpdateField)
	first := eventIDFor(t, "TEST-1", model.OpDelete)
	require.Contains(t, unloggedHolds(t), first)
	require.NoError(t, runUndelete("TEST-1"))
	pullFromHub(t, &stderr, hub)
	require.NoError(t, runDelete("TEST-1", true))
	second := eventIDFor(t, "TEST-1", model.OpDelete)
	require.NotEqual(t, first, second)
	push()

	require.GreaterOrEqual(t, hubIndex(hub, edit), 0, "the held edit is sent: %s", stderr.String())
	require.Greater(t, hubIndex(hub, second), hubIndex(hub, edit), "the later delete is sent, after the edit")
	require.False(t, hub.sent(first), "the delete made before the creation was logged is never sent")
	deleted := `SELECT COUNT(*) FROM nodes WHERE id = 'TEST-1' AND deleted_at IS NOT NULL`
	require.Equal(t, 1, countTestRows(t, deleted), "A holds TEST-1 deleted")
	require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
	require.Equal(t, 1, countTestRows(t, deleted), "peers converge deleted")
}

// TestPushLoop_UnloggedHold_LinkToLivePulledTask_ReleasedAndSent is review
// r1d X4: a link from a logged task to B's merge-imported TEST-1 is held;
// once A pulls B's creation of TEST-1, which is live here, the next push
// releases and sends the link; doctor passes and a fresh peer holds the
// link and quarantines nothing.
func TestPushLoop_UnloggedHold_LinkToLivePulledTask_ReleasedAndSent(t *testing.T) {
	ctx := context.Background()
	var stderr bytes.Buffer
	hub := newFakePushHub()
	push := func() {
		_, err := pushLoop(ctx, &stderr, hub, app.store)
		require.NoError(t, err, stderr.String())
	}
	importTeammateBoard(t, func() error { push(); return nil })
	require.NoError(t, runCreate("logged on A", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runDepAdd("TEST-2", "TEST-1", "related"))
	push()
	link := eventIDFor(t, "TEST-2", model.OpLinkDep)
	require.Contains(t, unloggedHolds(t), link)
	require.False(t, hub.sent(link))

	pullFromHub(t, &stderr, hub)
	push()
	require.True(t, hub.sent(link), "the held link to the live task is released and sent: %s", stderr.String())
	require.Empty(t, unloggedHolds(t))
	c := findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store), unloggedCheckName)
	require.True(t, c.Pass, c.Detail)
	require.Equal(t, "ok", c.Detail)

	require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
	require.Equal(t, 1, countTestRows(t,
		`SELECT COUNT(*) FROM dependencies WHERE from_id = 'TEST-2' AND to_id = 'TEST-1'`), "the peer holds the link")
}

// TestPushLoop_UnloggedHold_DeleteUndonePulledWithNoPushBetween_DeleteNeverSent:
// A's edit of B's merge-imported TEST-1 is held; A deletes TEST-1, undeletes
// it and pulls B's creation with no push in between, so the delete was never
// held. The next push releases and sends the edit, and holds the delete,
// made before the creation was logged, as retired instead of sending it
// (lead ruling r2 2: the held edit named TEST-1); a fresh peer holds TEST-1
// live with A's edit, as A does.
func TestPushLoop_UnloggedHold_DeleteUndonePulledWithNoPushBetween_DeleteNeverSent(t *testing.T) {
	ctx := context.Background()
	var stderr bytes.Buffer
	hub := newFakePushHub()
	push := func() {
		_, err := pushLoop(ctx, &stderr, hub, app.store)
		require.NoError(t, err, stderr.String())
	}
	importTeammateBoard(t, func() error { push(); return nil })
	require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
	push()
	edit := eventIDFor(t, "TEST-1", model.OpUpdateField)
	require.Contains(t, unloggedHolds(t), edit)
	require.NoError(t, runDelete("TEST-1", true))
	del := eventIDFor(t, "TEST-1", model.OpDelete)
	require.NoError(t, runUndelete("TEST-1"))
	pullFromHub(t, &stderr, hub)
	push()
	push()

	require.True(t, hub.sent(edit), "the live task's held edit is sent: %s", stderr.String())
	require.False(t, hub.sent(del), "the delete made before the creation was logged is never sent: %s", stderr.String())
	require.Contains(t, unloggedHolds(t)[del], "so this delete is never sent")
	require.Contains(t, stderr.String(), "push: held event")
	requireRetiredNote(t)

	require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
	n, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Nil(t, n.DeletedAt, "peers converge live")
	require.Equal(t, "edited on A", n.Title, "with A's edit")
}
