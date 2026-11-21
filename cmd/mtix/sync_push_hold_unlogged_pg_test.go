// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestPushLoop_EditOfUnloggedTask_RealHub_SecondCloneQuarantinesNothing is
// the PG-gated two-clone companion of
// TestPushLoop_EditOfUnloggedTask_HeldNotQuarantinedOnPeer
// (MTIX-112.17.16.37): clone A edits TEST-2, which its sync log has no
// creation for, and TEST-1, which it has, and pushes to a real hub. No
// event of TEST-2 reaches the hub; a second clone B pulls the hub, holds
// TEST-1 with its edit, does not hold TEST-2 and quarantines nothing.
func TestPushLoop_EditOfUnloggedTask_RealHub_SecondCloneQuarantinesNothing(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	var stderr bytes.Buffer
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("logged", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("imported", "", "", 3, "", "", "", "", ""))
	unlogTask(t, "TEST-2")
	_, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, stderr.String())
	require.NoError(t, runUpdate("TEST-2", "imported, edited", "", "", "", 0, "", ""))
	require.NoError(t, runComment("TEST-2", "a note", ""))
	require.NoError(t, runCreate("child", "TEST-2", "", 3, "", "", "", "", ""))
	require.NoError(t, runDepAdd("TEST-1", "TEST-2", "related"))
	require.NoError(t, runUpdate("TEST-1", "logged, edited", "", "", "", 0, "", ""))
	_, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, stderr.String())
	require.Len(t, unloggedHolds(t), 4, stderr.String())

	var onHub int
	require.NoError(t, pool.Inner().QueryRow(ctx, `SELECT COUNT(*) FROM sync_events
		WHERE node_id LIKE 'TEST-2%' OR op_type = 'link_dep'`).Scan(&onHub))
	require.Zero(t, onHub, "no event of the unlogged task reaches the hub")

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	require.Empty(t, quarantined(t), "the second clone quarantines nothing")
	n, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "logged, edited", n.Title)
	require.Equal(t, model.StatusOpen, n.Status)
	_, err = app.store.GetNode(ctx, "TEST-2")
	require.Error(t, err, "the second clone never holds the unlogged task")
}

// TestPushLoop_UnloggedHold_RealHub_ReleasedOnceItsCreationIsPulled is the
// PG-gated two-clone companion of
// TestPushLoop_UnloggedHold_ReleasedOnceItsCreationIsPulled (review r1 F1):
// B pushes TEST-1 to a real hub; A merge-imports B's board before it first
// syncs, edits TEST-1 and pushes (held), pulls B's creation and pushes
// again. The edit reaches the hub, doctor passes, and a third clone C holds
// TEST-1 with A's edit and quarantines nothing.
func TestPushLoop_UnloggedHold_RealHub_ReleasedOnceItsCreationIsPulled(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	var stderr bytes.Buffer
	push := func() error {
		_, err := pushLoop(ctx, &stderr, pool, app.store)
		return err
	}
	importTeammateBoard(t, push)
	require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
	require.NoError(t, push(), stderr.String())
	edit := eventIDFor(t, "TEST-1", model.OpUpdateField)
	require.Contains(t, unloggedHolds(t), edit, stderr.String())
	hubHas := func() int {
		var n int
		require.NoError(t, pool.Inner().QueryRow(ctx,
			`SELECT COUNT(*) FROM sync_events WHERE event_id = $1`, edit).Scan(&n))
		return n
	}
	require.Zero(t, hubHas(), "the held edit is not on the hub")

	_, _, err := pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	require.NoError(t, push(), stderr.String())
	require.Equal(t, 1, hubHas(), "the released edit reaches the hub: %s", stderr.String())
	require.Empty(t, unloggedHolds(t))
	r := appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store)
	require.True(t, findCheck(t, r, unloggedCheckName).Pass)
	require.True(t, findCheck(t, r, heldPushCheckName).Pass)

	t.Setenv(sqlite.AuthorIDEnv, "agent-c")
	initTestApp(t)
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	require.Empty(t, quarantined(t), "the third clone quarantines nothing")
	n, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "edited on A", n.Title, "A's edit reaches the third clone")
}

// TestPushLoop_UnloggedHold_RealHub_FixThenPull_OldTaskNeverSent is the
// PG-gated companion of TestPushLoop_UnloggedHold_FixThenPull_OldTaskNeverSent
// (review r1b S1, lead ruling option a): A edits B's merge-imported TEST-1
// (held), follows doctor's fix (re-creates it as TEST-2, deletes TEST-1),
// pushes, pulls B's creation of TEST-1 and pushes again. Nothing of A's old
// TEST-1 reaches the hub; a third clone C holds B's TEST-1 live and A's
// TEST-2 and quarantines nothing.
func TestPushLoop_UnloggedHold_RealHub_FixThenPull_OldTaskNeverSent(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	var stderr bytes.Buffer
	push := func() error {
		_, err := pushLoop(ctx, &stderr, pool, app.store)
		return err
	}
	importTeammateBoard(t, push)
	require.NoError(t, runUpdate("TEST-1", "edited on A", "", "", "", 0, "", ""))
	require.NoError(t, push(), stderr.String())
	require.NoError(t, runCreate("edited on A", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runDelete("TEST-1", true))
	own := ownEventsOf(t, "TEST-1")
	require.Len(t, own, 2, "A's edit and delete of TEST-1")
	require.NoError(t, push(), stderr.String())
	_, _, err := pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	require.NoError(t, push(), stderr.String())

	for _, id := range own {
		var n int
		require.NoError(t, pool.Inner().QueryRow(ctx,
			`SELECT COUNT(*) FROM sync_events WHERE event_id = $1`, id).Scan(&n))
		require.Zero(t, n, "A's event %s of the old task never reaches the hub: %s", id, stderr.String())
	}
	requireRetiredNote(t)

	t.Setenv(sqlite.AuthorIDEnv, "agent-c")
	initTestApp(t)
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	require.Empty(t, quarantined(t), "the third clone quarantines nothing")
	old, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err, "the teammate's task stays live on the third clone")
	require.Equal(t, "shared", old.Title)
	require.Nil(t, old.DeletedAt)
	recreated, err := app.store.GetNode(ctx, "TEST-2")
	require.NoError(t, err)
	require.Equal(t, "edited on A", recreated.Title)
}
