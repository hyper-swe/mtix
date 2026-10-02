// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The two-replica renumber collision of MTIX-95.37, against a real hub.
//
// INVARIANT under test: no event is ever applied to a node whose uid differs
// from the event's uid, and no event of a task whose creation the hub
// renumbered travels or applies under the old number. Replica A creates TEST-1
// and works on it offline; replica B creates its own TEST-1 and pushes first;
// A pushes. A's work must land entirely under A's new number on every replica
// and B's task must stay untouched.

// collisionOp is one kind of event A makes about its task (and, for the
// dependency cases, about a second task) before the hub renumbers the task.
type collisionOp struct {
	name string
	do   func(t *testing.T)
}

// collisionOps lists every dependent event kind: update, claim, unclaim,
// status change, comment, annotation, child create, dependency add and
// remove in both directions, and delete.
func collisionOps() []collisionOp {
	return []collisionOp{
		{"update", func(t *testing.T) {
			require.NoError(t, runUpdate("TEST-1", "A retitled", "", "", "", 0, "", ""))
		}},
		{"claim", func(t *testing.T) { require.NoError(t, runClaim("TEST-1", "agent-a")) }},
		{"unclaim", func(t *testing.T) {
			require.NoError(t, runClaim("TEST-1", "agent-a"))
			require.NoError(t, runUnclaim("TEST-1", "done with it"))
		}},
		{"status", func(t *testing.T) {
			require.NoError(t, runCancel("TEST-1", "not needed", false))
		}},
		{"comment", func(t *testing.T) { require.NoError(t, runComment("TEST-1", "A says hi", "")) }},
		{"annotate", func(t *testing.T) { require.NoError(t, runAnnotate("TEST-1", "A note")) }},
		{"child", func(t *testing.T) {
			require.NoError(t, runCreate("A child", "TEST-1", "", 3, "", "", "", "", ""))
			require.NoError(t, runUpdate("TEST-1.1", "A child retitled", "", "", "", 0, "", ""))
		}},
		{"dep add from", func(t *testing.T) { require.NoError(t, runDepAdd("TEST-1", "TEST-2", "blocks")) }},
		{"dep add to", func(t *testing.T) { require.NoError(t, runDepAdd("TEST-2", "TEST-1", "blocks")) }},
		{"dep remove", func(t *testing.T) {
			require.NoError(t, runDepAdd("TEST-2", "TEST-1", "blocks"))
			require.NoError(t, runDepRemove("TEST-2", "TEST-1", "blocks"))
		}},
		{"delete", func(t *testing.T) { require.NoError(t, runDelete("TEST-1", true)) }},
	}
}

// collisionRun is the replicas of one collision: A's and B's app state and
// the uid of A's task.
type collisionRun struct {
	pool       *transport.Pool
	appA, appB appContext
	uidA       string
}

// runCollision plays the scenario: A creates TEST-1 and TEST-2, runs op and
// (filler > 0) makes filler more tasks between the creation and op, so the
// op goes out in a later batch; B creates its own TEST-1 and pushes; A
// pushes.
func runCollision(t *testing.T, op collisionOp, filler int) collisionRun {
	t.Helper()
	ctx := context.Background()
	r := collisionRun{pool: openCmdHub(t)}
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	r.appA = app
	require.NoError(t, runCreate("A task", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("A other", "", "", 3, "", "", "", "", ""))
	for i := 0; i < filler; i++ {
		require.NoError(t, runCreate(fmt.Sprintf("filler %d", i), "", "", 3, "", "", "", "", ""))
	}
	op.do(t)
	r.uidA = nodeUID(t, "TEST-1")

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	r.appB = app
	require.NoError(t, runCreate("B task", "", "", 3, "", "", "", "", ""))
	var stderr bytes.Buffer
	_, err := pushLoop(ctx, &stderr, r.pool, app.store)
	require.NoError(t, err, stderr.String())

	app = r.appA
	_, err = pushLoop(ctx, &stderr, r.pool, app.store)
	require.NoError(t, err, stderr.String())
	return r
}

// nodeUID returns the uid of the live or deleted node id in the current app.
func nodeUID(t *testing.T, id string) string {
	t.Helper()
	var uid string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, id).Scan(&uid))
	return uid
}

// fingerprint describes the task with uid and its subtree in the current
// app: the number, title, status, assignee, annotations and deletion of each
// node, and every dependency touching the subtree, so replicas compare equal
// only when they hold the same work under the same numbers.
func fingerprint(t *testing.T, uid string) string {
	t.Helper()
	ctx := context.Background()
	var root string
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT id FROM nodes WHERE uid = ? AND uid <> ''`, uid).Scan(&root))
	var sb strings.Builder
	rows, err := app.store.Query(ctx, `
		SELECT id, title, status, COALESCE(assignee, ''), COALESCE(annotations, ''),
		       COALESCE(deleted_at, '') <> ''
		FROM nodes WHERE id = ? OR id LIKE ? ESCAPE '\' ORDER BY id`, root, root+".%")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id, title, status, assignee, ann string
		var deleted bool
		require.NoError(t, rows.Scan(&id, &title, &status, &assignee, &ann, &deleted))
		// Annotation ids and times differ per replica; their count and text do not.
		fmt.Fprintf(&sb, "%s|%s|%s|%s|ann=%d|del=%v\n", id, title, status, assignee,
			strings.Count(ann, `"text"`), deleted)
	}
	require.NoError(t, rows.Err())
	deps, err := app.store.Query(ctx, `
		SELECT from_id, to_id, dep_type FROM dependencies
		WHERE from_id = ? OR to_id = ? OR from_id LIKE ? ESCAPE '\' OR to_id LIKE ? ESCAPE '\'
		ORDER BY from_id, to_id`, root, root, root+".%", root+".%")
	require.NoError(t, err)
	defer func() { _ = deps.Close() }()
	for deps.Next() {
		var f, to, ty string
		require.NoError(t, deps.Scan(&f, &to, &ty))
		fmt.Fprintf(&sb, "dep %s>%s %s\n", f, to, ty)
	}
	require.NoError(t, deps.Err())
	return sb.String()
}

// subtreeUIDs returns the uid of the task with uid and of every task below it
// in the current app, soft-deleted ones included.
func subtreeUIDs(t *testing.T, uid string) []string {
	t.Helper()
	var root string
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT id FROM nodes WHERE uid = ? AND uid <> ''`, uid).Scan(&root))
	rows, err := app.store.Query(context.Background(),
		`SELECT uid FROM nodes WHERE (id = ? OR id LIKE ? ESCAPE '\') AND uid <> ''`, root, root+".%")
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var u string
		require.NoError(t, rows.Scan(&u))
		out = append(out, u)
	}
	require.NoError(t, rows.Err())
	return out
}

// requireBIntact asserts B's own TEST-1 is exactly what B made: no claim, no
// retitle, no child, no dependency, no annotation, not deleted.
func requireBIntact(t *testing.T) {
	t.Helper()
	node, err := app.store.GetNode(context.Background(), "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B task", node.Title)
	require.Equal(t, model.StatusOpen, node.Status)
	require.Empty(t, node.Assignee)
	require.Empty(t, node.Annotations)
	var kids, deps int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM nodes WHERE id LIKE 'TEST-1.%'`).Scan(&kids))
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM dependencies WHERE from_id = 'TEST-1' OR to_id = 'TEST-1'`).Scan(&deps))
	require.Zero(t, kids, "no phantom child under B's task")
	require.Zero(t, deps, "no dependency on B's task")
}

// TestRenumberCollision_RealHub_NoEventAppliesUnderTheOldNumber is the
// MTIX-95.37 reproduction and its class: for every dependent event kind, in
// the same batch as the renumbered creation and in a later one, B's task is
// never touched by A's events, A's work lands under its new number on A, on B
// and on a fresh clone, and the hub holds no event of A's task under the old
// number.
func TestRenumberCollision_RealHub_NoEventAppliesUnderTheOldNumber(t *testing.T) {
	for _, filler := range []int{0, pushBatchSize} {
		for _, op := range collisionOps() {
			t.Run(fmt.Sprintf("%s/filler=%d", op.name, filler), func(t *testing.T) {
				ctx := context.Background()
				r := runCollision(t, op, filler)
				var stderr bytes.Buffer

				// No event of any task in A's subtree is on the hub under the old number.
				app = r.appA
				uids := subtreeUIDs(t, r.uidA)
				var oldNumber int
				require.NoError(t, r.pool.Inner().QueryRow(ctx, `
					SELECT COUNT(*) FROM sync_events
					WHERE uid = ANY($1) AND (node_id = 'TEST-1' OR node_id LIKE 'TEST-1.%')`,
					uids).Scan(&oldNumber))
				require.Zero(t, oldNumber, "no event of A's subtree reaches the hub under the old number")

				app = r.appA
				_, _, err := pullLoop(ctx, testIngest(&stderr), r.pool, app.store, transport.PullCursor{}, 100)
				require.NoError(t, err, stderr.String())
				fpA := fingerprint(t, r.uidA)
				require.NotContains(t, fpA, "TEST-1|", "A's task left the old number")
				mine, err := app.store.GetNode(ctx, "TEST-1")
				require.NoError(t, err)
				require.Equal(t, "B task", mine.Title, "A holds B's task at the contested number")
				require.Equal(t, model.StatusOpen, mine.Status)

				app = r.appB
				_, _, err = pullLoop(ctx, testIngest(&stderr), r.pool, app.store, transport.PullCursor{}, 100)
				require.NoError(t, err, stderr.String())
				requireBIntact(t)
				require.Equal(t, fpA, fingerprint(t, r.uidA), "B holds A's work under the new number")

				t.Setenv(sqlite.AuthorIDEnv, "agent-c")
				initTestApp(t)
				_, _, err = cloneLoop(ctx, &stderr, r.pool, app.store, transport.PullCursor{}, 100)
				require.NoError(t, err, stderr.String())
				requireBIntact(t)
				require.Equal(t, fpA, fingerprint(t, r.uidA), "a fresh clone holds A's work under the new number")
			})
		}
	}
}

// TestRenumberCollision_RealHub_PullBeforePush_TeammateTaskReachesA: A pulls
// B's creation of TEST-1 while its own creation of TEST-1 is still pending.
// The pulled creation must not count as applied and be lost: it is held and
// retried, and once A's push moves A's task to a new number, A's next pull
// brings in B's task at TEST-1 with B's later edit. B's task is never touched
// by A's events, and a fresh clone holds both.
func TestRenumberCollision_RealHub_PullBeforePush_TeammateTaskReachesA(t *testing.T) {
	ctx := context.Background()
	pool := openCmdHub(t)
	var stderr bytes.Buffer
	t.Setenv(sqlite.AuthorIDEnv, "agent-a")
	initTestApp(t)
	appA := app
	require.NoError(t, runCreate("A task", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runClaim("TEST-1", "agent-a"))
	uidA := nodeUID(t, "TEST-1")

	t.Setenv(sqlite.AuthorIDEnv, "agent-b")
	initTestApp(t)
	appB := app
	require.NoError(t, runCreate("B task", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runUpdate("TEST-1", "B retitled", "", "", "", 0, "", ""))
	_, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, stderr.String())

	app = appA
	_, err = pullThenSweep(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	mine, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "A task", mine.Title, "A's pending task keeps its number until the hub decides")
	require.Equal(t, uidA, nodeUID(t, "TEST-1"))

	_, err = pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err, stderr.String())
	_, err = pullThenSweep(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	theirs, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B retitled", theirs.Title, "B's task reaches A, edit included")
	require.Empty(t, theirs.Assignee, "A's claim did not land on B's task")
	fpA := fingerprint(t, uidA)

	app = appB
	_, _, err = pullLoop(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	b, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "B retitled", b.Title)
	require.Empty(t, b.Assignee)
	require.Equal(t, fpA, fingerprint(t, uidA), "B holds A's work under the new number")

	t.Setenv(sqlite.AuthorIDEnv, "agent-c")
	initTestApp(t)
	_, _, err = cloneLoop(ctx, &stderr, pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	require.Equal(t, fpA, fingerprint(t, uidA), "a fresh clone holds A's work under the new number")
}

// TestRenumberCollision_RealHub_PullBeforePush_NumberRefsInPayloads is the
// review probe of MTIX-95.37: B pushes its TEST-1, a child TEST-1.1 and a link
// from TEST-2 to TEST-1, all naming TEST-1 by number in their payloads; A,
// whose own TEST-1 is still pending, pulls first. Nothing of B's may land on
// A's task, whatever field names the number (a creation's parent_id, a link's
// depends_on_node_id), and after A's push moves its task, A's pull brings all
// of B's work in at TEST-1, with B's dependency on B's task.
func TestRenumberCollision_RealHub_PullBeforePush_NumberRefsInPayloads(t *testing.T) {
	tests := []struct {
		name string
		do   func(t *testing.T)
	}{
		{"child under the number", func(t *testing.T) {
			require.NoError(t, runCreate("B child", "TEST-1", "", 3, "", "", "", "", ""))
		}},
		{"link to the number", func(t *testing.T) {
			require.NoError(t, runCreate("B other", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runDepAdd("TEST-2", "TEST-1", "blocks"))
		}},
		{"root then deleted", func(t *testing.T) {
			require.NoError(t, runDelete("TEST-1", false))
		}},
		{"child then deleted", func(t *testing.T) {
			require.NoError(t, runCreate("B child", "TEST-1", "", 3, "", "", "", "", ""))
			require.NoError(t, runDelete("TEST-1.1", false))
		}},
		{"child then updated", func(t *testing.T) {
			require.NoError(t, runCreate("B child", "TEST-1", "", 3, "", "", "", "", ""))
			require.NoError(t, runUpdate("TEST-1.1", "B child retitled", "", "", "", 0, "", ""))
		}},
		{"child then claimed", func(t *testing.T) {
			require.NoError(t, runCreate("B child", "TEST-1", "", 3, "", "", "", "", ""))
			require.NoError(t, runClaim("TEST-1.1", "agent-b"))
		}},
		{"child then commented", func(t *testing.T) {
			require.NoError(t, runCreate("B child", "TEST-1", "", 3, "", "", "", "", ""))
			require.NoError(t, runComment("TEST-1.1", "B note", ""))
		}},
		{"child and link", func(t *testing.T) {
			require.NoError(t, runCreate("B other", "", "", 3, "", "", "", "", ""))
			require.NoError(t, runCreate("B child", "TEST-1", "", 3, "", "", "", "", ""))
			require.NoError(t, runDepAdd("TEST-2", "TEST-1", "blocks"))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			pool := openCmdHub(t)
			var stderr bytes.Buffer
			t.Setenv(sqlite.AuthorIDEnv, "agent-a")
			initTestApp(t)
			appA := app
			require.NoError(t, runCreate("A task", "", "", 3, "", "", "", "", ""))
			uidA := nodeUID(t, "TEST-1")

			t.Setenv(sqlite.AuthorIDEnv, "agent-b")
			initTestApp(t)
			appB := app
			require.NoError(t, runCreate("B task", "", "", 3, "", "", "", "", ""))
			tt.do(t)
			_, err := pushLoop(ctx, &stderr, pool, app.store)
			require.NoError(t, err, stderr.String())
			fpB := fingerprint(t, nodeUID(t, "TEST-1"))

			app = appA
			_, err = pullThenSweep(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
			require.NoError(t, err, stderr.String())
			require.Equal(t, "TEST-1|A task|open||ann=0|del=false\n", fingerprint(t, uidA),
				"A's pending task is exactly what A made")
			var kids, deps int
			require.NoError(t, app.store.QueryRow(ctx, `SELECT COUNT(*) FROM nodes WHERE id LIKE 'TEST-1.%'`).Scan(&kids))
			require.NoError(t, app.store.QueryRow(ctx, `SELECT COUNT(*) FROM dependencies`).Scan(&deps))
			require.Zero(t, kids, "no child of B's under A's task")
			require.Zero(t, deps, "no dependency of B's on A's task")
			mine, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			require.Equal(t, model.StatusOpen, mine.Status, "A's task was not auto-blocked")

			_, err = pushLoop(ctx, &stderr, pool, app.store)
			require.NoError(t, err, stderr.String())
			_, err = pullThenSweep(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
			require.NoError(t, err, stderr.String())
			var theirUID string
			require.NoError(t, app.store.QueryRow(ctx,
				`SELECT uid FROM nodes WHERE id = 'TEST-1'`).Scan(&theirUID))
			require.Equal(t, fpB, fingerprint(t, theirUID), "B's task, child and link arrive at TEST-1 intact")
			require.NotEqual(t, uidA, theirUID)

			app = appB
			_, err = pullThenSweep(ctx, testIngest(&stderr), pool, app.store, transport.PullCursor{}, 100)
			require.NoError(t, err, stderr.String())
			require.Equal(t, fpB, fingerprint(t, nodeUID(t, "TEST-1")), "B's own task is untouched")
		})
	}
}
