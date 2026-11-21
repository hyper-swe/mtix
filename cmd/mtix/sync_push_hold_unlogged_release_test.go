// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The end of a push hold of a task the sync log had no creation for
// (MTIX-112.17.16.37, review r1 F1). The task's creation can reach the log
// after the hold, by pull: a teammate pushed the creation of the same task,
// which this clone merge-imported from the teammate's board before it first
// synced. The next push releases the hold and sends the held edits; doctor
// never calls such a hold retired. A hold whose task mtix gc purged is
// retired: it stays as a record and is never sent.

// exportBoardFile writes the current replica's board to a file, as mtix
// export does, and returns its path.
func exportBoardFile(t *testing.T) string {
	t.Helper()
	data, err := service.NewAdminService(app.store).Export(context.Background(), false)
	require.NoError(t, err)
	body, err := json.Marshal(data)
	require.NoError(t, err)
	file := filepath.Join(t.TempDir(), "board.json")
	require.NoError(t, os.WriteFile(file, body, 0o600))
	return file
}

// importTeammateBoard makes B create TEST-1 and push it to hub (whose
// pusher is push), exports B's board, then makes a new replica A (as app)
// that merge-imports the board before it first syncs (and before a hub is
// set up for it), so A holds TEST-1 with B's uid and no logged creation of
// it.
func importTeammateBoard(t *testing.T, push func() error) {
	t.Helper()
	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	require.NoError(t, runCreate("shared", "", "", 3, "", "", "", "", ""))
	require.NoError(t, push())
	file := exportBoardFile(t)

	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	// A imports before any hub is set up for it (a merge import into a store
	// that syncs is refused), then takes the hub DSN, if any, again.
	dsn := os.Getenv(transport.EnvDSN)
	t.Setenv(transport.EnvDSN, "")
	require.NoError(t, runImport(file, importFlags{mode: "merge", confirm: true}))
	t.Setenv(transport.EnvDSN, dsn)
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE op_type = 'create_node'`),
		"the merge import logged no creation of TEST-1")
}

// TestPushLoop_UnloggedHold_ReleasedOnceItsCreationIsPulled is review r1
// probe F1 against a fake hub: A edits TEST-1, which its log has no
// creation for, so push holds the edit; A then pulls B's creation of
// TEST-1. Doctor fails (the edit is not on the hub yet) and says the next
// push sends it; that push releases and sends the edit, doctor passes, and
// a fresh peer holds TEST-1 with A's edit and quarantines nothing.
func TestPushLoop_UnloggedHold_ReleasedOnceItsCreationIsPulled(t *testing.T) {
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
	require.Contains(t, unloggedHolds(t), edit, stderr.String())
	require.False(t, hub.sent(edit))

	_, _, err := pullLoop(ctx, testIngest(&stderr), &fakeLateHub{pullEvents: hub.events},
		app.store, transport.PullCursor{}, 1000)
	require.NoError(t, err, stderr.String())
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE op_type = 'create_node'`),
		"the pull logged B's creation of TEST-1")
	r := appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store)
	c := findCheck(t, r, unloggedCheckName)
	require.False(t, c.Pass, "the held edit is not on the hub yet: %s", c.Detail)
	require.Contains(t, c.Detail, "1 held events whose task the sync log now has the creation of")
	require.Contains(t, c.Detail, "the next mtix sync push sends them")
	require.NotContains(t, c.Detail, "deleted")
	require.Contains(t, c.Fix, "mtix sync push")

	require.NoError(t, push(), stderr.String())
	require.True(t, hub.sent(edit), "the released edit reaches the hub: %s", stderr.String())
	require.Equal(t, "pushed", syncStatusOf(t, edit))
	require.Empty(t, unloggedHolds(t))
	require.Contains(t, stderr.String(), "push: released held event")
	r = appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store)
	c = findCheck(t, r, unloggedCheckName)
	require.True(t, c.Pass, c.Detail)
	require.Equal(t, "ok", c.Detail)
	require.True(t, findCheck(t, r, heldPushCheckName).Pass)

	require.Empty(t, freshPeerPull(t, hub.events), "the fresh peer quarantines nothing")
	n, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "edited on A", n.Title, "A's edit reaches the peer")
}

// TestPushLoop_UnloggedHold_TaskPurgedByGC_RetiredNeverSent: a held edit of
// an unlogged task whose task is then deleted and purged by mtix gc stays
// held (nothing of it is left to send and peers never held it); doctor
// passes and counts it as a record.
func TestPushLoop_UnloggedHold_TaskPurgedByGC_RetiredNeverSent(t *testing.T) {
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("logged", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("imported", "", "", 3, "", "", "", "", ""))
	unlogTask(t, "TEST-2")
	require.NoError(t, runUpdate("TEST-2", "imported, edited", "", "", "", 0, "", ""))
	hub := newFakePushHub()
	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())
	edit := eventIDFor(t, "TEST-2", model.OpUpdateField)
	require.Contains(t, unloggedHolds(t), edit)

	require.NoError(t, runDelete("TEST-2", true))
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE nodes SET deleted_at = '2000-01-01T00:00:00Z' WHERE id = 'TEST-2'`)
	require.NoError(t, err)
	require.NoError(t, runConfigSet("data.soft_delete_retention", "1ns"))
	require.NoError(t, runGC())
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE id = 'TEST-2'`), "gc purged the task")

	_, err = pushLoop(ctx, &stderr, hub, app.store)
	require.NoError(t, err, stderr.String())
	require.False(t, hub.sent(edit), "the edit of the purged task is never sent")
	require.Contains(t, unloggedHolds(t), edit, "it stays held as a record")
	c := findCheck(t, appendHeldPushCheck(ctx, DoctorReport{OverallPass: true}, app.store), unloggedCheckName)
	require.True(t, c.Pass, c.Detail)
	require.Contains(t, c.Detail, "held events of deleted tasks")
}
