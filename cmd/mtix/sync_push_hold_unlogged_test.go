// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Edits of a task the sync log has no creation for (MTIX-112.17.16.37,
// founder decision DESK-1.371, option D-hold).
//
// A task can sit on the board with no create_node in the sync log: a merge
// import made before the store first synced writes no journal, and so does
// a task made before the journal existed. Its later edits used to be
// journaled and pushed; a peer, which never received the task, quarantined
// every one of them, and the pusher saw nothing wrong. Push now holds every
// event about such a task (the task itself, any task below it, a link that
// names it), doctor names each held task with the fix, and a task that has
// a logged creation pushes as before.

// unlogTask removes the creation of the task id from the local sync log,
// the state a merge import made before the store synced leaves.
func unlogTask(t *testing.T, id string) {
	t.Helper()
	res, err := app.store.WriteDB().ExecContext(context.Background(), `
		DELETE FROM sync_events WHERE op_type = 'create_node'
		  AND uid = (SELECT uid FROM nodes WHERE id = ?)`, id)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.Equal(t, int64(1), n, "the creation of %s was in the log", id)
}

// freshPeerPull makes a new replica, pulls every event the hub holds into
// it and returns it (as app) with its quarantined events.
func freshPeerPull(t *testing.T, events []*model.SyncEvent) map[string]sqlite.QuarantinedEvent {
	t.Helper()
	var stderr bytes.Buffer
	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	_, _, err := pullLoop(context.Background(), testIngest(&stderr), &fakeLateHub{pullEvents: events},
		app.store, transport.PullCursor{}, 1000)
	require.NoError(t, err, stderr.String())
	return quarantined(t)
}

// unloggedHolds returns the reasons of the local push holds of unlogged
// tasks, keyed by event id.
func unloggedHolds(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for id, q := range quarantined(t) {
		if q.Source == "push" && strings.HasPrefix(q.Reason, holdUnloggedPrefix) {
			out[id] = q.Reason
		}
	}
	return out
}

// TestPushLoop_EditOfUnloggedTask_HeldNotQuarantinedOnPeer is the ticket's
// repro, one row per journaled edit: TEST-2 is on A's board with no logged
// creation, TEST-1 has one. Every event of the edit that names TEST-2's line
// is held on A with a reason naming TEST-2, none reaches the hub, and a fresh
// peer pulling the hub quarantines nothing and holds TEST-1 only. An edit of
// TEST-1 in the same push reaches the peer.
func TestPushLoop_EditOfUnloggedTask_HeldNotQuarantinedOnPeer(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T)
		// held lists the ops of TEST-1 whose events must be held too.
		held []model.OpType
	}{
		{"update", func(t *testing.T) {
			require.NoError(t, runUpdate("TEST-2", "retitled", "", "", "", 0, "", ""))
		}, nil},
		{"comment", func(t *testing.T) { require.NoError(t, runComment("TEST-2", "a note", "")) }, nil},
		{"create under", func(t *testing.T) {
			require.NoError(t, runCreate("child", "TEST-2", "", 3, "", "", "", "", ""))
			require.NoError(t, runUpdate("TEST-2.1", "child retitled", "", "", "", 0, "", ""))
		}, nil},
		{"dep add to it", func(t *testing.T) { require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks")) },
			[]model.OpType{model.OpLinkDep}},
		{"dep add from it", func(t *testing.T) { require.NoError(t, runDepAdd("TEST-2", "TEST-1", "related")) }, nil},
		{"dep remove to it", func(t *testing.T) {
			require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks"))
			require.NoError(t, runDepRemove("TEST-1", "TEST-2", "blocks"))
		}, []model.OpType{model.OpLinkDep, model.OpUnlinkDep}},
		{"claim", func(t *testing.T) { require.NoError(t, runClaim("TEST-2", "agent-a")) }, nil},
		{"transition", func(t *testing.T) {
			require.NoError(t, runTransition("TEST-2", model.StatusCancelled, "not needed"))
		}, nil},
		{"delete", func(t *testing.T) { require.NoError(t, runDelete("TEST-2", false)) }, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			t.Setenv(sqlite.AuthorIDEnv, "agent-a")
			initTestApp(t)
			require.NoError(t, runCreate("logged", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("imported", "", "", 3, "", "", "", "", ""))
			unlogTask(t, "TEST-2")
			hub := newFakePushHub()
			var stderr bytes.Buffer
			_, err := pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err, stderr.String())

			tt.edit(t)
			require.NoError(t, runUpdate("TEST-1", "logged retitled", "", "", "", 0, "", ""))
			_, err = pushLoop(ctx, &stderr, hub, app.store)
			require.NoError(t, err, stderr.String())

			held := unloggedHolds(t)
			require.NotEmpty(t, held, "the edit of the unlogged task is held: %s", stderr.String())
			for id, reason := range held {
				require.Contains(t, reason, "TEST-2", "the reason names the unlogged task")
				for _, e := range hub.events {
					require.NotEqual(t, id, e.EventID, "a held event never reaches the hub")
				}
			}
			require.Contains(t, stderr.String(), "no logged creation", "push names the hold")
			for _, op := range tt.held {
				require.Contains(t, held, eventIDFor(t, "TEST-1", op), "the %s of TEST-1 to TEST-2 is held", op)
			}

			peerQuarantine := freshPeerPull(t, hub.events)
			require.Empty(t, peerQuarantine, "the fresh peer quarantines nothing")
			n, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			require.Equal(t, "logged retitled", n.Title, "the logged task's edit arrives")
			_, err = app.store.GetNode(ctx, "TEST-2")
			require.Error(t, err, "the peer never holds the unlogged task")
		})
	}
}

// TestPushLoop_MergeImportedTask_EditHeld is the ticket's repro through a
// real merge import: A imports a file that adds TEST-2 before A first
// syncs, so the log has no creation for it; A pushes, edits TEST-2 and
// pushes again. The edit is held and a fresh peer quarantines nothing.
func TestPushLoop_MergeImportedTask_EditHeld(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-c")
	initTestApp(t)
	require.NoError(t, runCreate("first", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("from the file", "", "", 3, "", "", "", "", ""))
	data, err := service.NewAdminService(app.store).Export(ctx, false)
	require.NoError(t, err)
	body, err := json.Marshal(data)
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(file, body, 0o600))

	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("first", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runImport(file, importFlags{mode: "merge", confirm: true, forceRename: true}))
	var imported string
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT id FROM nodes WHERE title = 'from the file'`).Scan(&imported))
	var logged int
	require.NoError(t, app.store.QueryRow(ctx, `SELECT COUNT(*) FROM sync_events e JOIN nodes n
		ON e.uid = n.uid WHERE n.id = ? AND e.op_type = 'create_node'`, imported).Scan(&logged))
	require.Zero(t, logged, "the merge import logged no creation of %s", imported)

	hub := newFakePushHub()
	var stderr bytes.Buffer
	_, err = pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())
	require.NoError(t, runUpdate(imported, "edited after the import", "", "", "", 0, "", ""))
	_, err = pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())
	require.Len(t, unloggedHolds(t), 1, stderr.String())

	require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
}

// TestSyncDoctor_UnloggedTask_NamedWithFix: doctor fails while an edit of an
// unlogged task is held, names the task and gives the re-create fix with
// its exact commands; the generic held-push check leaves those holds to it;
// the JSON report carries the same name, detail and fix.
func TestSyncDoctor_UnloggedTask_NamedWithFix(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("parent", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("imported", "TEST-1", "", 3, "", "", "", "", ""))
	unlogTask(t, "TEST-1.1")
	require.NoError(t, runUpdate("TEST-1.1", "retitled", "", "", "", 0, "", ""))
	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, newFakePushHub(), app.store)
	require.NoError(t, err)

	r := appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store)
	require.False(t, r.OverallPass)
	c := findCheck(t, r, unloggedCheckName)
	require.False(t, c.Pass)
	require.Contains(t, c.Detail, "TEST-1.1")
	for _, want := range []string{"mtix show TEST-1.1 --json", "mtix create", "--under TEST-1",
		"mtix delete TEST-1.1 --cascade"} {
		require.Contains(t, c.Fix, want)
	}
	generic := findCheck(t, r, heldPushCheckName)
	require.True(t, generic.Pass, "the generic check leaves unlogged holds to %q: %s", unloggedCheckName, generic.Detail)

	body, err := json.Marshal(c)
	require.NoError(t, err)
	require.Contains(t, string(body), `"fix":`)
	require.Contains(t, string(body), "TEST-1.1")
}

// TestSyncDoctor_UnloggedTask_RecreateClearsIt is the fix path: re-create
// the task as a new one (logged), delete the old one; the held events stay
// as a record but doctor passes, the new task pushes and a fresh peer holds
// it with nothing quarantined. Restoring the old task fails doctor again.
func TestSyncDoctor_UnloggedTask_RecreateClearsIt(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("logged", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("imported", "", "", 3, "", "", "", "", ""))
	unlogTask(t, "TEST-2")
	require.NoError(t, runUpdate("TEST-2", "imported, edited", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("imported child", "TEST-2", "", 3, "", "", "", "", ""))
	hub := newFakePushHub()
	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err)
	before := findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store), unloggedCheckName)
	require.False(t, before.Pass)
	require.Contains(t, before.Detail, ": TEST-2 (2 held events)", "the edit and the child creation are held")

	require.NoError(t, runCreate("imported, edited", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runDelete("TEST-2", true))
	_, err = pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())

	r := appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store)
	c := findCheck(t, r, unloggedCheckName)
	require.True(t, c.Pass, c.Detail)
	require.Contains(t, c.Detail, "deleted")
	generic := findCheck(t, r, heldPushCheckName)
	require.True(t, generic.Pass, generic.Detail)
	require.NotEmpty(t, unloggedHolds(t), "the held events stay as a record")

	require.NoError(t, runUndelete("TEST-2"))
	require.False(t, findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store),
		unloggedCheckName).Pass, "a restored unlogged task fails doctor again")

	require.Empty(t, freshPeerPull(t, hub.events))
	n, err := app.store.GetNode(ctx, "TEST-3")
	require.NoError(t, err)
	require.Equal(t, "imported, edited", n.Title, "the re-created task reaches the peer")
}

// TestPushLoop_UnloggedHold_DoesNotHoldOtherLinks: a held creation under an
// unlogged task does not join the held creations that make later links
// wait, so a link between two logged tasks still pushes.
func TestPushLoop_UnloggedHold_DoesNotHoldOtherLinks(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("logged", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("imported", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("logged too", "", "", 3, "", "", "", "", ""))
	unlogTask(t, "TEST-2")
	require.NoError(t, runCreate("child", "TEST-2", "", 3, "", "", "", "", ""))
	require.NoError(t, runDepAdd("TEST-1", "TEST-3", "blocks"))
	hub := newFakePushHub()
	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err)
	link := eventIDFor(t, "TEST-1", model.OpLinkDep)
	require.Equal(t, "pushed", syncStatusOf(t, link), stderr.String())
	child := eventIDFor(t, "TEST-2.1", model.OpCreateNode)
	require.Contains(t, unloggedHolds(t), child)

	// A later push starts from the recorded holds: the held child creation
	// still makes no later link wait, and a later event of the child is
	// held as unlogged, not as a dependent of that creation.
	require.NoError(t, runDepRemove("TEST-1", "TEST-3", "blocks"))
	require.NoError(t, runUpdate("TEST-2.1", "child retitled", "", "", "", 0, "", ""))
	_, err = pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err)
	unlink := eventIDFor(t, "TEST-1", model.OpUnlinkDep)
	require.Equal(t, "pushed", syncStatusOf(t, unlink), stderr.String())
	require.Contains(t, unloggedHolds(t), eventIDFor(t, "TEST-2.1", model.OpUpdateField))
}
