// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Retired holds of tasks the sync log had no creation for
// (MTIX-112.17.16.37, reviews r1b and r1c; lead rulings). A clone
// merge-imported a teammate's task before it first synced, so its edits are
// held. A delete made here before a pull brings the teammate's creation of
// the same task (the doctor's re-create fix, or mtix gc) stays local for
// good, and so do the task's other held events while it stays deleted: the
// teammate's task stays live on every peer. A delete made after the pull is
// an ordinary delete and is sent; an undeleted task sends its held edits.

// ownEventsOf returns the ids of the events of node id other than its
// creation in the current replica's log: before a pull, this replica's own.
func ownEventsOf(t *testing.T, id string) []string {
	t.Helper()
	rows, err := app.store.Query(context.Background(),
		`SELECT event_id FROM sync_events WHERE node_id = ? AND op_type <> 'create_node'`, id)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var ids []string
	for rows.Next() {
		var e string
		require.NoError(t, rows.Scan(&e))
		ids = append(ids, e)
	}
	require.NoError(t, rows.Err())
	return ids
}

// requireRetiredNote checks that doctor's check passes with the retired
// note and nothing for the next push to send.
func requireRetiredNote(t *testing.T) {
	t.Helper()
	c := findCheck(t, appendHeldPushCheck(context.Background(), DoctorReport{OverallPass: true}, app.store),
		unloggedCheckName)
	require.True(t, c.Pass, c.Detail)
	require.Contains(t, c.Detail, "held events of deleted tasks")
	require.NotContains(t, c.Detail, "the next mtix sync push sends them")
}

// TestPushLoop_UnloggedHold_FixThenPull_OldTaskNeverSent is review r1b S1
// against a fake hub: A edits TEST-1 (B's task, merge-imported before A
// first synced), push holds the edit, A follows doctor's fix (re-creates it
// as TEST-2, deletes TEST-1) and then pulls B's creation of TEST-1. No push
// ever sends A's edit or delete of TEST-1; doctor passes with the retired
// note at every step; a fresh peer holds B's TEST-1 live and A's TEST-2 and
// quarantines nothing.
func TestPushLoop_UnloggedHold_FixThenPull_OldTaskNeverSent(t *testing.T) {
	tests := []struct {
		name string
		// pushBeforePull pushes once between the fix and the pull.
		pushBeforePull bool
	}{
		{"push, then pull", true},
		{"pull right after the fix", false},
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
			require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
			push()
			require.Contains(t, unloggedHolds(t), eventIDFor(t, "TEST-1", model.OpUpdateField))

			require.NoError(t, runCreate("edited on A", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runDelete("TEST-1", true))
			own := ownEventsOf(t, "TEST-1")
			require.Len(t, own, 2, "A's edit and delete of TEST-1")
			if tt.pushBeforePull {
				push()
				requireRetiredNote(t)
			}
			_, _, err := pullLoop(ctx, testIngest(&stderr), &fakeLateHub{pullEvents: hub.events},
				app.store, transport.PullCursor{}, 1000)
			require.NoError(t, err, stderr.String())
			push()
			push()

			for _, id := range own {
				require.False(t, hub.sent(id), "A's event %s of the old task never reaches the hub: %s", id, stderr.String())
				require.Contains(t, unloggedHolds(t), id, "it stays held as a record")
			}
			require.Contains(t, unloggedHolds(t)[eventIDFor(t, "TEST-1", model.OpDelete)],
				"was deleted here before the sync log held its creation", "the delete's reason is true")
			require.True(t, hub.sent(eventIDFor(t, "TEST-2", model.OpCreateNode)), "the re-created task is sent")
			require.NotContains(t, stderr.String(), "released held event")
			requireRetiredNote(t)

			require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
			old, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err, "the teammate's task stays live on the peer")
			require.Equal(t, "shared", old.Title)
			require.Nil(t, old.DeletedAt)
			recreated, err := app.store.GetNode(ctx, "TEST-2")
			require.NoError(t, err)
			require.Equal(t, "edited on A", recreated.Title)
		})
	}
}

// TestPushLoop_UnloggedTaskPurgedByGC_TeammateTaskStaysLive is review r1b
// S2: A deletes TEST-1 (B's task, merge-imported, no logged creation) and
// mtix gc purges it before A pushes. The delete's task has no node and no
// logged creation, so push holds it as retired and sends nothing of TEST-1,
// with or without an earlier held edit; doctor passes with the retired
// note. After A pulls B's creation, A holds TEST-1 live again: the next
// push never sends the delete (made before the pull) but sends the held
// edit of the now live task (lead ruling r1d 2); a fresh peer holds TEST-1
// live and quarantines nothing.
func TestPushLoop_UnloggedTaskPurgedByGC_TeammateTaskStaysLive(t *testing.T) {
	for _, editFirst := range []bool{true, false} {
		name := map[bool]string{true: "edit held first", false: "no hold before the purge"}[editFirst]
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			var stderr bytes.Buffer
			hub := newFakePushHub()
			push := func() {
				_, err := pushLoop(ctx, &stderr, hub, app.store)
				require.NoError(t, err, stderr.String())
			}
			importTeammateBoard(t, func() error { push(); return nil })
			if editFirst {
				require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
				push()
			}
			require.NoError(t, runDelete("TEST-1", true))
			_, err := app.store.WriteDB().ExecContext(ctx,
				`UPDATE nodes SET deleted_at = '2000-01-01T00:00:00Z' WHERE id = 'TEST-1'`)
			require.NoError(t, err)
			require.NoError(t, runConfigSet("data.soft_delete_retention", "1ns"))
			require.NoError(t, runGC())
			require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE id = 'TEST-1'`), "gc purged the task")
			own := ownEventsOf(t, "TEST-1")
			push()
			for _, id := range own {
				require.False(t, hub.sent(id), "A's event %s of the purged task is never sent: %s", id, stderr.String())
				require.Contains(t, unloggedHolds(t), id)
			}
			requireRetiredNote(t)

			_, _, err = pullLoop(ctx, testIngest(&stderr), &fakeLateHub{pullEvents: hub.events},
				app.store, transport.PullCursor{}, 1000)
			require.NoError(t, err, stderr.String())
			push()
			del := eventIDFor(t, "TEST-1", model.OpDelete)
			require.False(t, hub.sent(del), "the delete made before the pull is never sent: %s", stderr.String())
			require.Contains(t, unloggedHolds(t), del)
			for _, id := range own {
				if id != del {
					require.True(t, hub.sent(id), "the task is live here again, so its held edit is sent: %s",
						stderr.String())
				}
			}
			requireRetiredNote(t)
			back, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			require.Nil(t, back.DeletedAt, "A holds the teammate's task live, as every peer does")

			require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
			n, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err, "the teammate's task stays live on the peer")
			require.Nil(t, n.DeletedAt)
		})
	}
}

// TestPushLoop_UnloggedHold_NoPulledCreation_SecondPushLeavesItAlone is
// review r1b S3: while the task still has no logged creation, a later push
// neither releases the hold (no released line) nor records it again: its
// first_seen and attempts stay as the first push stamped them.
func TestPushLoop_UnloggedHold_NoPulledCreation_SecondPushLeavesItAlone(t *testing.T) {
	ctx := context.Background()
	var stderr bytes.Buffer
	hub := newFakePushHub()
	push := func() error {
		_, err := pushLoop(ctx, &stderr, hub, app.store)
		return err
	}
	importTeammateBoard(t, push)
	require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
	require.NoError(t, push(), stderr.String())
	edit := eventIDFor(t, "TEST-1", model.OpUpdateField)
	first, ok := quarantined(t)[edit]
	require.True(t, ok, "the edit is held: %s", stderr.String())

	app.store.SetClock(func() time.Time { return time.Now().Add(time.Hour) })
	stderr.Reset()
	require.NoError(t, push(), stderr.String())
	again, ok := quarantined(t)[edit]
	require.True(t, ok, "the edit is still held")
	require.NotContains(t, stderr.String(), "released held event", "a still unlogged hold is not released")
	require.Equal(t, first.FirstSeen, again.FirstSeen, "the hold is not recorded again")
	require.Equal(t, first.Attempts, again.Attempts)
	require.Equal(t, first.Reason, again.Reason)
	require.False(t, hub.sent(edit))
}

// pullFromHub pulls every event hub holds into the current replica.
func pullFromHub(t *testing.T, stderr *bytes.Buffer, hub *fakePushHub) {
	t.Helper()
	_, _, err := pullLoop(context.Background(), testIngest(stderr), &fakeLateHub{pullEvents: hub.events},
		app.store, transport.PullCursor{}, 1000)
	require.NoError(t, err, stderr.String())
}

// hubIndex returns the position of event id among the events hub received,
// or -1.
func hubIndex(hub *fakePushHub, id string) int {
	for i, e := range hub.events {
		if e.EventID == id {
			return i
		}
	}
	return -1
}

// TestPushLoop_UnloggedHold_DeleteAfterPulledCreation_SentInOrder is review
// r1c S2 (lead ruling r1d 1): A's edit of B's merge-imported TEST-1 is
// held, A pulls B's creation, and only then deletes TEST-1. That delete is
// an ordinary delete of a shared task: the next push sends the held edit
// and then the delete, doctor passes with nothing retired, and a fresh peer
// holds TEST-1 deleted, as A does.
func TestPushLoop_UnloggedHold_DeleteAfterPulledCreation_SentInOrder(t *testing.T) {
	ctx := context.Background()
	var stderr bytes.Buffer
	hub := newFakePushHub()
	push := func() error {
		_, err := pushLoop(ctx, &stderr, hub, app.store)
		return err
	}
	importTeammateBoard(t, push)
	require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
	require.NoError(t, push(), stderr.String())
	edit := eventIDFor(t, "TEST-1", model.OpUpdateField)
	require.Contains(t, unloggedHolds(t), edit)
	pullFromHub(t, &stderr, hub)
	require.NoError(t, runDelete("TEST-1", true))
	del := eventIDFor(t, "TEST-1", model.OpDelete)
	c := findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store), unloggedCheckName)
	require.False(t, c.Pass, "the held edit is not sent yet: %s", c.Detail)
	require.Contains(t, c.Detail, "the next mtix sync push sends them")

	require.NoError(t, push(), stderr.String())
	require.GreaterOrEqual(t, hubIndex(hub, edit), 0, "the held edit is sent: %s", stderr.String())
	require.Greater(t, hubIndex(hub, del), hubIndex(hub, edit), "the delete is sent after the edit")
	require.Empty(t, unloggedHolds(t))
	c = findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store), unloggedCheckName)
	require.True(t, c.Pass, c.Detail)
	require.Equal(t, "ok", c.Detail)

	deleted := `SELECT COUNT(*) FROM nodes WHERE id = 'TEST-1' AND deleted_at IS NOT NULL`
	require.Equal(t, 1, countTestRows(t, deleted), "A holds TEST-1 deleted")
	require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
	require.Equal(t, 1, countTestRows(t, deleted), "peers converge deleted")
}

// TestPushLoop_UnloggedHold_UndeletedTask_EditsSentDeleteKept is review
// r1c S2 (lead ruling r1d 2): A edits B's merge-imported TEST-1 (held),
// deletes it and undeletes it, then pulls B's creation, before or after the
// undelete. The task is live here, so push sends the held edit and never
// the delete (it was undone); doctor fails while the edit waits, then
// passes; a fresh peer holds TEST-1 live with A's edit.
func TestPushLoop_UnloggedHold_UndeletedTask_EditsSentDeleteKept(t *testing.T) {
	for _, undeleteFirst := range []bool{true, false} {
		name := map[bool]string{true: "undelete, then pull", false: "pull, then undelete"}[undeleteFirst]
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			var stderr bytes.Buffer
			hub := newFakePushHub()
			push := func() error {
				_, err := pushLoop(ctx, &stderr, hub, app.store)
				return err
			}
			importTeammateBoard(t, push)
			require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
			require.NoError(t, runDelete("TEST-1", true))
			require.NoError(t, push(), stderr.String())
			edit := eventIDFor(t, "TEST-1", model.OpUpdateField)
			del := eventIDFor(t, "TEST-1", model.OpDelete)
			require.Contains(t, unloggedHolds(t), del)
			if undeleteFirst {
				require.NoError(t, runUndelete("TEST-1"))
				c := findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store), unloggedCheckName)
				require.False(t, c.Pass, "an undeleted unlogged task fails doctor again: %s", c.Detail)
			}
			pullFromHub(t, &stderr, hub)
			if !undeleteFirst {
				require.NoError(t, runUndelete("TEST-1"))
			}
			c := findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store), unloggedCheckName)
			require.False(t, c.Pass, c.Detail)
			require.Contains(t, c.Detail, "1 held events whose task the sync log now has the creation of")

			require.NoError(t, push(), stderr.String())
			require.True(t, hub.sent(edit), "the live task's held edit is sent: %s", stderr.String())
			require.False(t, hub.sent(del), "the undone delete is never sent")
			require.Contains(t, unloggedHolds(t), del)
			requireRetiredNote(t)

			require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
			n, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			require.Nil(t, n.DeletedAt, "peers converge live")
			require.Equal(t, "edited on A", n.Title, "with A's edit")
		})
	}
}
