// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Tests of the events behind a quarantined creation (MTIX-95.37).
// INVARIANT: a pulled event about a task whose create_node is quarantined is
// quarantined with it, whatever its op, and the retry applies it after the
// creation; it is never applied as a no-op (a delete of a task the replica
// does not hold yet) or dropped, so the retry of the creation cannot bring
// back what the teammate deleted or lose what the teammate changed.

// snapshotOf describes, in the current store, the tasks with the given uids
// and every dependency between tasks of the set: number, title, status,
// assignee, annotation count and deletion, so replicas compare equal only
// when they hold the same work.
func snapshotOf(t *testing.T, st *sqlite.Store, uids []string) string {
	t.Helper()
	ctx := context.Background()
	var sb strings.Builder
	for _, uid := range uids {
		var id, title, status, assignee, ann string
		var deleted bool
		err := st.QueryRow(ctx, `SELECT id, title, status, COALESCE(assignee, ''), COALESCE(annotations, ''),
			COALESCE(deleted_at, '') <> '' FROM nodes WHERE uid = ? AND uid <> ''`, uid).
			Scan(&id, &title, &status, &assignee, &ann, &deleted)
		require.NoError(t, err, "task %s is held", uid)
		fmt.Fprintf(&sb, "%s|%s|%s|%s|ann=%d|del=%v\n", id, title, status, assignee,
			strings.Count(ann, `"text"`), deleted)
	}
	rows, err := st.Query(ctx, `SELECT from_id, to_id, dep_type FROM dependencies ORDER BY from_id, to_id`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var f, to, ty string
		require.NoError(t, rows.Scan(&f, &to, &ty))
		fmt.Fprintf(&sb, "dep %s>%s %s\n", f, to, ty)
	}
	require.NoError(t, rows.Err())
	return sb.String()
}

// behindOps lists, for every op type, what the teammate does to TEST-1 after
// creating it (and TEST-2, which A does not hold either, for the links).
func behindOps() []struct {
	name string
	do   func(ctx context.Context, t *testing.T, b *sqlite.Store)
} {
	type op = func(ctx context.Context, t *testing.T, b *sqlite.Store)
	child := func(ctx context.Context, t *testing.T, b *sqlite.Store) {
		require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-1.1", "TEST-1", 1, 1, "B child")))
	}
	title := "B retitled"
	return []struct {
		name string
		do   op
	}{
		{"update", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Title: &title}))
		}},
		{"claim", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.ClaimNode(ctx, "TEST-1", "agent-b"))
		}},
		{"unclaim", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.ClaimNode(ctx, "TEST-1", "agent-b"))
			require.NoError(t, b.UnclaimNode(ctx, "TEST-1", "handoff", "agent-b"))
		}},
		{"status", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.TransitionStatus(ctx, "TEST-1", model.StatusDeferred, "later", "agent-b"))
		}},
		{"comment", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.SetAnnotations(ctx, "TEST-1", []model.Annotation{
				{ID: "01HZZZZZZZZZZZZZZZZZZZZZZZ", Author: "agent-b", Text: "B note", CreatedAt: time.Now().UTC()}}))
		}},
		{"root deleted", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.DeleteNode(ctx, "TEST-1", false, "agent-b"))
		}},
		{"child created", child},
		{"child created then deleted", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			child(ctx, t, b)
			require.NoError(t, b.DeleteNode(ctx, "TEST-1.1", false, "agent-b"))
		}},
		{"child created then updated", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			child(ctx, t, b)
			require.NoError(t, b.UpdateNode(ctx, "TEST-1.1", &store.NodeUpdate{Title: &title}))
		}},
		{"grandchild created", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			child(ctx, t, b)
			require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-1.1.1", "TEST-1.1", 2, 1, "B grandchild")))
		}},
		{"link to it", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "B other")))
			require.NoError(t, b.AddDependency(ctx, &model.Dependency{FromID: "TEST-2", ToID: "TEST-1", DepType: model.DepTypeBlocks}))
		}},
		{"link from it", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "B other")))
			require.NoError(t, b.AddDependency(ctx, &model.Dependency{FromID: "TEST-1", ToID: "TEST-2", DepType: model.DepTypeBlocks}))
		}},
		{"unlink", func(ctx context.Context, t *testing.T, b *sqlite.Store) {
			require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "B other")))
			require.NoError(t, b.AddDependency(ctx, &model.Dependency{FromID: "TEST-2", ToID: "TEST-1", DepType: model.DepTypeBlocks}))
			require.NoError(t, b.RemoveDependency(ctx, "TEST-2", "TEST-1", model.DepTypeBlocks))
		}},
	}
}

// TestPull_EventsBehindQuarantinedCreate_FollowItAndApplyAfterIt: A holds its
// own pending TEST-1, so B's creation of TEST-1 is quarantined; every later
// event of B about it, of every op type, is quarantined behind it (never
// applied as a no-op); after A's push moves A's task, one quarantine retry
// applies them all in order and A holds exactly what B holds.
func TestPull_EventsBehindQuarantinedCreate_FollowItAndApplyAfterIt(t *testing.T) {
	for _, tt := range behindOps() {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			initTestApp(t)
			require.NoError(t, runCreate("A task", "", "", 3, "", "", "", "", ""))
			local := nodeUID(t, "TEST-1")

			b, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
			require.NoError(t, err)
			t.Cleanup(func() { _ = b.Close() })
			require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "B task")))
			tt.do(ctx, t, b)
			events, err := readPendingBatch(ctx, b, 100)
			require.NoError(t, err)
			var uids []string
			for _, e := range events {
				if e.OpType == model.OpCreateNode {
					uids = append(uids, e.UID)
				}
			}

			held, err := applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull, events)
			require.NoError(t, err)
			for _, e := range events {
				if e.NodeID == "TEST-2" && e.OpType == model.OpCreateNode {
					require.NotContains(t, held, e.EventID, "an unrelated creation applies")
					continue
				}
				require.Contains(t, held, e.EventID, "%s of %s is held behind the quarantined creation", e.OpType, e.NodeID)
			}
			mine, err := app.store.GetNode(ctx, "TEST-1")
			require.NoError(t, err)
			require.Equal(t, "A task", mine.Title, "A's pending task is untouched")

			_, err = app.store.RenumberForHubRejection(ctx, local)
			require.NoError(t, err)
			retry, err := retryQuarantinedEvents(ctx, testIngest(nil), app.store, 100)
			require.NoError(t, err)
			require.Equal(t, 0, retry.held, "nothing stays held once the number is free")
			require.Empty(t, quarantined(t))

			got := snapshotOf(t, app.store, uids)
			want := snapshotOf(t, b, uids)
			require.Equal(t, want, got, "A holds exactly what B holds, deletes included")
		})
	}
}

// TestRetryQuarantinedEvents_CreateCannotApplyYet_DependentsStayQueued: while
// the creation still fails (the number is still A's), its dependents stay in
// the quarantine, each with an attempt counted, and apply on a later retry.
func TestRetryQuarantinedEvents_CreateCannotApplyYet_DependentsStayQueued(t *testing.T) {
	ctx := context.Background()
	initTestApp(t)
	require.NoError(t, runCreate("A task", "", "", 3, "", "", "", "", ""))
	local := nodeUID(t, "TEST-1")
	b, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = b.Close() })
	require.NoError(t, b.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "B task")))
	require.NoError(t, b.DeleteNode(ctx, "TEST-1", false, "agent-b"))
	events, err := readPendingBatch(ctx, b, 100)
	require.NoError(t, err)
	_, err = applyPullBatch(ctx, testIngest(nil), app.store, quarantineSourcePull, events)
	require.NoError(t, err)

	retry, err := retryQuarantinedEvents(ctx, testIngest(nil), app.store, 100)
	require.NoError(t, err)
	require.Equal(t, 0, retry.applied)
	held := quarantined(t)
	require.Len(t, held, 2)
	for _, q := range held {
		require.GreaterOrEqual(t, q.Attempts, 2, "the retry counted an attempt for %s", q.OpType)
	}

	_, err = app.store.RenumberForHubRejection(ctx, local)
	require.NoError(t, err)
	retry, err = retryQuarantinedEvents(ctx, testIngest(nil), app.store, 100)
	require.NoError(t, err)
	require.Equal(t, 2, retry.applied)
	require.Equal(t, 1, countTestRows(t, `SELECT COUNT(*) FROM nodes WHERE id = 'TEST-1' AND deleted_at IS NOT NULL`),
		"the teammate's task stays deleted")
}
