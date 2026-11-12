// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// TestPushLoop_HeldCreationWhoseUIDNoNodeHas_RealHub_TeammateTaskIntact is
// the review r1 S1 and S2 reproduction against a real hub (MTIX-95.12 run
// 2): A's creation of TEST-1 is size-held; B creates and pushes its own
// TEST-1; A's task then loses the uid its creation carries (a merge import
// adopts the file's uid), or its creation was queued without one; A
// retitles TEST-1, creates a child under it and pushes; B pulls. B's task
// must keep its title, get no child, and no A event for TEST-1 or its
// subtree may reach the hub.
func TestPushLoop_HeldCreationWhoseUIDNoNodeHas_RealHub_TeammateTaskIntact(t *testing.T) {
	tests := []struct {
		name    string
		loseUID func(t *testing.T)
	}{
		{"creation queued without a uid", func(t *testing.T) { queueWithoutUID(t, "TEST-1") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pool := openCmdHub(t)
			ctx := context.Background()
			var stderr bytes.Buffer
			t.Setenv(sqlite.AuthorIDEnv, "agent-a")
			initTestApp(t)
			appA := app
			require.NoError(t, runCreate("A's task", "", "", 3, "", overWireCap(), "", "", ""))
			_, err := pushLoop(ctx, &stderr, pool, app.store)
			require.NoError(t, err)

			t.Setenv(sqlite.AuthorIDEnv, "agent-b")
			initTestApp(t)
			appB := app
			require.NoError(t, runCreate("B's own task", "", "", 3, "", "", "", "", ""))
			_, err = pushLoop(ctx, &stderr, pool, app.store)
			require.NoError(t, err)

			app = appA
			t.Setenv(sqlite.AuthorIDEnv, "agent-a")
			tt.loseUID(t)
			require.NoError(t, runUpdate("TEST-1", "A retitled its task", "", "", "", 0, "", ""))
			require.NoError(t, runCreate("A's child", "TEST-1", "", 3, "", "", "", "", ""))
			_, err = pushLoop(ctx, &stderr, pool, app.store)
			require.NoError(t, err)

			app = appB
			_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
			require.NoError(t, err)
			node, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			require.Equal(t, "B's own task", node.Title)
			require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE parent_id = 'TEST-1'`),
				"B's task gets no child from A")
			var fromA int
			require.NoError(t, pool.Inner().QueryRow(ctx,
				`SELECT COUNT(*) FROM sync_events WHERE (node_id = 'TEST-1' OR node_id LIKE 'TEST-1.%')
				 AND author_id = 'agent-a'`).Scan(&fromA))
			require.Zero(t, fromA, "no event of A's held task or its subtree reaches the hub")
			app = appA
			requireHeld := func(node string, op model.OpType) {
				_, held := quarantined(t)[eventIDFor(t, node, op)]
				require.True(t, held, "A's %s %s is held", node, op)
			}
			requireHeld("TEST-1", model.OpUpdateField)
			requireHeld("TEST-1.1", model.OpCreateNode)
		})
	}
}

// A project that connects to a hub never merges a file: a merge import
// writes no sync journal, so it is refused before any write
// (MTIX-112.17.16.16), and so is an automatic import that changes a task the
// store holds. Each real-hub test below used to adopt a file uid for a task
// through one of those imports; it now asserts the refusal, that the task
// keeps its own uid and creation, and that the pushed state stays intact.

// ownUID returns the uid the local store holds for nodeID.
func ownUID(t *testing.T, nodeID string) string {
	t.Helper()
	node, err := app.store.GetNode(context.Background(), nodeID)
	require.NoError(t, err)
	require.NotEmpty(t, node.UID)
	return node.UID
}

// TestPushLoop_MergeWithQueuedCreationOnAHubProject_IsRefusedAndTeammateTaskIntact
// is the MTIX-95.31.16 reproduction on a real hub, kept as a refusal: A's
// creation of TEST-1 is queued, B creates and pushes its own TEST-1, and a
// merge import that would give A's TEST-1 the file's uid is refused because
// the project connects to a hub. A's task keeps its uid and its creation,
// nothing of A reaches the hub under another uid, the push is not stalled,
// and B's task keeps its title.
func TestPushLoop_MergeWithQueuedCreationOnAHubProject_IsRefusedAndTeammateTaskIntact(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	var stderr bytes.Buffer
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	appA := app
	require.NoError(t, runCreate("A's task", "", "", 3, "", "", "", "", ""))
	own := ownUID(t, "TEST-1")
	create := eventIDFor(t, "TEST-1", model.OpCreateNode)

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	appB := app
	require.NoError(t, runCreate("B's own task", "", "", 3, "", "", "", "", ""))
	_, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	app = appA
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	_, err = tryAdoptFileUID(t, "TEST-1")
	require.ErrorIs(t, err, sqlite.ErrMergeOnSyncedStore, "the merge is refused: the project connects to a hub")
	require.Equal(t, own, ownUID(t, "TEST-1"), "A's task keeps its own uid")
	require.Equal(t, create, eventIDFor(t, "TEST-1", model.OpCreateNode), "A's queued creation is unchanged")
	_, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, "the push is not stalled")

	var other int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE author_id = 'agent-a' AND uid <> $1`, own).Scan(&other))
	require.Zero(t, other, "no event of A reaches the hub under another uid")
	app = appB
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	node, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B's own task", node.Title, "B's task is not changed by A")
}

// TestPushLoop_MergeMidPushOnAHubProject_IsRefusedAndTaskStaysOnOneUID is
// the MTIX-95.31.16 review S1 probe on a real hub, kept as a refusal: a merge
// import tries to adopt a uid for a task between the hub committing a push
// batch and the local mark (the markPushedFailKey seam). The project connects
// to a hub, so the merge is refused before it reaches the push lock; the
// creation is marked pushed and never sent again: the hub holds one creation
// of the task, under the uid it was sent with.
func TestPushLoop_MergeMidPushOnAHubProject_IsRefusedAndTaskStaysOnOneUID(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("A's task", "", "", 3, "", "", "", "", ""))
	own := ownUID(t, "TEST-1")
	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	var adoptErr error
	tried := false
	midPush := context.WithValue(ctx, markPushedFailKey{}, func([]string) error {
		if !tried { // once: a later batch of the same push must not try again
			tried = true
			_, adoptErr = tryAdoptFileUID(t, "TEST-1")
		}
		return nil
	})
	var stderr bytes.Buffer
	_, err = pushLoop(midPush, &stderr, pool, app.store)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
	require.True(t, tried, "the merge was tried mid-push")
	require.ErrorIs(t, adoptErr, sqlite.ErrMergeOnSyncedStore, "refused because the project connects to a hub")
	require.Equal(t, own, ownUID(t, "TEST-1"), "the task keeps its uid")

	_, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)
	var creates, uids int
	var uid string
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT uid), COALESCE(MIN(uid), '') FROM sync_events
		 WHERE author_id = 'agent-a' AND op_type = 'create_node'`).Scan(&creates, &uids, &uid))
	require.Equal(t, 1, creates, "the creation reaches the hub once")
	require.Equal(t, 1, uids)
	require.Equal(t, own, uid, "under the uid it was sent with")
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`))
}

// TestPushLoop_AutoImportWithQueuedCreationOnAHubProject_IsRefusedAndPushNotStalled
// is the MTIX-95.31.16 review probe on a real hub for the automatic import
// after a git pull, kept as a refusal: A's creation of TEST-1 is queued, B
// creates and pushes its own TEST-1, and A's automatic import of a file that
// gives TEST-1 another uid is refused, because the project connects to a hub
// and the file changes a task the store holds. A's task keeps its uid, the
// push is not stalled, no event of A reaches the hub under another uid, and
// B's task keeps its title.
func TestPushLoop_AutoImportWithQueuedCreationOnAHubProject_IsRefusedAndPushNotStalled(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	var stderr bytes.Buffer
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	appA := app
	require.NoError(t, runCreate("A's task", "", "", 3, "", "", "", "", ""))
	own := ownUID(t, "TEST-1")

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	appB := app
	require.NoError(t, runCreate("B's own task", "", "", 3, "", "", "", "", ""))
	_, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	app = appA
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	_, err = autoImportFileUID(t, "TEST-1")
	require.ErrorIs(t, err, service.ErrAutoImportRefused, "the import is refused: the project connects to a hub")
	require.Equal(t, own, ownUID(t, "TEST-1"), "A's task keeps its own uid")
	_, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, "the push is not stalled")

	var other int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE author_id = 'agent-a' AND uid <> $1`, own).Scan(&other))
	require.Zero(t, other, "no event of A reaches the hub under another uid")
	app = appB
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	node, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B's own task", node.Title)
}
