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
		{"uid adopted by a merge", func(t *testing.T) { adoptFileUID(t, "TEST-1") }},
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

// TestPushLoop_UIDAdoptedAfterCreationQueued_RealHub_TeammateTaskIntact is
// the MTIX-95.31.16 reproduction on a real hub: A's creation of TEST-1 is
// queued, B creates and pushes its own TEST-1, and a merge import then
// gives A's TEST-1 the file's uid. A retitles TEST-1, creates a child and
// pushes; B pulls. Every event A sends for its task, the creation included,
// reaches the hub under the adopted uid, so the retitle and the child are
// applied to A's task and never to B's, which keeps its title and gets no
// child.
func TestPushLoop_UIDAdoptedAfterCreationQueued_RealHub_TeammateTaskIntact(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	var stderr bytes.Buffer
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	appA := app
	require.NoError(t, runCreate("A's task", "", "", 3, "", "", "", "", ""))

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	appB := app
	require.NoError(t, runCreate("B's own task", "", "", 3, "", "", "", "", ""))
	_, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	app = appA
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	adopted := adoptFileUID(t, "TEST-1")
	require.NoError(t, runUpdate("TEST-1", "A retitled its task", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("A's child", "TEST-1", "", 3, "", "", "", "", ""))
	_, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	var other int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE author_id = 'agent-a' AND node_id = 'TEST-1' AND uid <> $1`,
		adopted).Scan(&other))
	require.Zero(t, other, "no event of A's task reaches the hub under another uid")

	app = appB
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	node, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B's own task", node.Title, "A's retitle is not applied to B's task")
	var title string
	require.NoError(t, app.store.QueryRow(ctx, `SELECT title FROM nodes WHERE uid = ?`, adopted).Scan(&title))
	require.Equal(t, "A retitled its task", title, "the retitle reaches the task the adopted uid names")
}

// TestPushLoop_UIDAdoptedMidPush_RealHub_TaskStaysOnOneUID is the
// MTIX-95.31.16 review S1 probe on a real hub: a merge import tries to adopt
// a uid for a task between the hub committing a push batch and the local
// mark (the markPushedFailKey seam), while the push holds the push lock.
// The import is refused, so the creation is marked pushed and is never sent
// again: the hub holds one creation of the task, under the uid it was sent
// with, and one retitle after the adoption that follows the push.
func TestPushLoop_UIDAdoptedMidPush_RealHub_TaskStaysOnOneUID(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	require.NoError(t, runCreate("A's task", "", "", 3, "", "", "", "", ""))
	lock, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	var adoptErr error
	tried := false
	midPush := context.WithValue(ctx, markPushedFailKey{}, func([]string) error {
		if !tried { // once: a later batch of the same push must not adopt again
			tried = true
			_, adoptErr = tryAdoptFileUID(t, "TEST-1")
		}
		return nil
	})
	var stderr bytes.Buffer
	_, err = pushLoop(midPush, &stderr, pool, app.store)
	require.NoError(t, err)
	require.NoError(t, lock.Release())
	require.ErrorContains(t, adoptErr, "push is running")

	_, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)
	var creates, uids int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT COUNT(*), COUNT(DISTINCT uid) FROM sync_events WHERE author_id = 'agent-a' AND op_type = 'create_node'`).
		Scan(&creates, &uids))
	require.Equal(t, 1, creates, "the creation reaches the hub once")
	require.Equal(t, 1, uids)
	require.Zero(t, countTestRows(t, `SELECT COUNT(*) FROM sync_events WHERE sync_status = 'pending'`))
}

// TestPushLoop_UIDAdoptedByAutoImport_RealHub_PushNotStalled is the
// MTIX-95.31.16 review probe on a real hub for the automatic import after a
// git pull: A's creation of TEST-1 is queued, B creates and pushes its own
// TEST-1, A's automatic import then adopts a uid for TEST-1, and A retitles
// it, creates a child and pushes. Every event of A's task reaches the hub
// under the adopted uid, the push is not stalled by a renumber outcome it
// cannot resolve, and B's task keeps its title.
func TestPushLoop_UIDAdoptedByAutoImport_RealHub_PushNotStalled(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	var stderr bytes.Buffer
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	appA := app
	require.NoError(t, runCreate("A's task", "", "", 3, "", "", "", "", ""))

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	appB := app
	require.NoError(t, runCreate("B's own task", "", "", 3, "", "", "", "", ""))
	_, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	app = appA
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	adopted, err := autoImportFileUID(t, "TEST-1")
	require.NoError(t, err)
	require.NoError(t, runUpdate("TEST-1", "A retitled its task", "", "", "", 0, "", ""))
	require.NoError(t, runCreate("A's child", "TEST-1", "", 3, "", "", "", "", ""))
	_, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, "the push is not stalled")

	var other int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT COUNT(*) FROM sync_events WHERE author_id = 'agent-a' AND node_id = 'TEST-1' AND uid <> $1`,
		adopted).Scan(&other))
	require.Zero(t, other, "no event of A's task reaches the hub under another uid")
	app = appB
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	node, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B's own task", node.Title)
}
