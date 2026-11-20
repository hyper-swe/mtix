// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Guards of the move of an unlogged holder (MTIX-112.17.16.39): a holder, or
// any task under it, that the hub can know about is never moved, because its
// number would then differ between replicas; the move is recorded in the task
// history, re-addresses the pending events and finds a free number.

// applyBound applies events through the Store-bound applier, the one a pull
// uses, so the history of the replay is recorded.
func applyBound(t *testing.T, s *sqlite.Store, evs ...*model.SyncEvent) error {
	t.Helper()
	ctx := context.Background()
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		for _, e := range evs {
			if err := s.IdempotentApply(ctx, tx, e); err != nil {
				return err
			}
		}
		return nil
	})
}

// TestApply_HubCreateOnNumberOfUnloggedParent_LoggedChild_HoldsTheHubCreate: the
// holder itself has no create in the log, but a task under it does and the hub
// holds it (pushed) or applied it: moving the parent would renumber that
// child here only. Nothing moves, and the hub create is not dropped silently:
// it is refused (so it is quarantined and retried), with a reason that names
// the held number and the local holder.
func TestApply_HubCreateOnNumberOfUnloggedParent_LoggedChild_HoldsTheHubCreate(t *testing.T) {
	for _, status := range []string{"pushed", "applied"} {
		t.Run(status, func(t *testing.T) {
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
			_, err := raw.Exec(`DELETE FROM sync_events WHERE op_type = 'create_node' AND node_id = 'MTIX-1'`)
			require.NoError(t, err)
			_, err = raw.Exec(`UPDATE sync_events SET sync_status = ? WHERE op_type = 'create_node' AND node_id = 'MTIX-1.1'`, status)
			require.NoError(t, err)
			holder := nodeRow(t, raw, "MTIX-1")["uid"]
			create := foreignCreate(t, "MTIX-1", hubUID, "hub's task")

			err = applyEventsInTx(s, []*model.SyncEvent{create})

			require.ErrorIs(t, err, sqlite.ErrNumberHeldByLoggedSubtree)
			require.Contains(t, err.Error(), "MTIX-1", "the reason names the held number")
			require.Contains(t, err.Error(), holder, "the reason names the local holder")
			require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM applied_events WHERE event_id = ?`, create.EventID),
				"a held creation is not recorded as applied")
			require.Equal(t, "Test MTIX-1", nodeRow(t, raw, "MTIX-1")["title"], "the holder keeps its number")
			require.Equal(t, "MTIX-1", rowByUID(t, raw, nodeRow(t, raw, "MTIX-1.1")["uid"])["parent"])
			require.Equal(t, 2, countRows(t, raw, `SELECT COUNT(*) FROM nodes`))
			require.Zero(t, countRows(t, raw, `SELECT COUNT(*) FROM nodes WHERE uid = ?`, hubUID))
		})
	}
}

// TestApply_HubCreateOnNumberOfUnloggedParent_UidlessLoggedChild_HoldsTheHubCreate:
// a task under the holder that has no uid is matched by its number: a pushed
// create of it holds the hub create too.
func TestApply_HubCreateOnNumberOfUnloggedParent_UidlessLoggedChild_HoldsTheHubCreate(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
	_, err := raw.Exec(`DELETE FROM sync_events WHERE op_type = 'create_node' AND node_id = 'MTIX-1'`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE sync_events SET sync_status = 'pushed' WHERE op_type = 'create_node' AND node_id = 'MTIX-1.1'`)
	require.NoError(t, err)
	_, err = raw.Exec(`UPDATE nodes SET uid = '' WHERE id = 'MTIX-1.1'`)
	require.NoError(t, err)

	err = applyEventsInTx(s, []*model.SyncEvent{foreignCreate(t, "MTIX-1", hubUID, "hub's task")})

	require.ErrorIs(t, err, sqlite.ErrNumberHeldByLoggedSubtree, "the number of a uid-less child that was pushed keeps the holder in place")
	require.Equal(t, "Test MTIX-1", nodeRow(t, raw, "MTIX-1")["title"])
}

// TestApply_HubCreateOnNumberOfUnloggedParent_PendingChild_MovesBoth: a child
// whose creation is still pending is not known to the hub, so it moves with
// its parent; its pending creation follows under the new number and parent.
func TestApply_HubCreateOnNumberOfUnloggedParent_PendingChild_MovesBoth(t *testing.T) {
	s, raw := mutationTestStore(t)
	mustCreateNode(t, s, "MTIX-1", "")
	mustCreateNode(t, s, "MTIX-1.1", "MTIX-1")
	_, err := raw.Exec(`DELETE FROM sync_events WHERE op_type = 'create_node' AND node_id = 'MTIX-1'`)
	require.NoError(t, err)
	parentUID := nodeRow(t, raw, "MTIX-1")["uid"]
	childUID := nodeRow(t, raw, "MTIX-1.1")["uid"]

	require.NoError(t, applyEventsInTx(s, []*model.SyncEvent{foreignCreate(t, "MTIX-1", hubUID, "hub's task")}))

	require.Equal(t, "MTIX-1", rowByUID(t, raw, hubUID)["id"], "the hub's task takes the number")
	require.Equal(t, "MTIX-2", rowByUID(t, raw, parentUID)["id"])
	child := rowByUID(t, raw, childUID)
	require.Equal(t, "MTIX-2.1", child["id"])
	require.Equal(t, "MTIX-2", child["parent"])
	require.Equal(t, 1, countRows(t, raw,
		`SELECT COUNT(*) FROM sync_events WHERE op_type = 'create_node' AND uid = ? AND node_id = 'MTIX-2.1' AND sync_status = 'pending'`,
		childUID), "the child's creation is pending under the new number")
}

// TestApply_HubCreateOnNumberOfLocalTask_LoggedHolderVariants_KeepFirstWriter:
// each key the guard matches a logged holder by is enough on its own.
func TestApply_HubCreateOnNumberOfLocalTask_LoggedHolderVariants_KeepFirstWriter(t *testing.T) {
	tests := []struct {
		name  string
		setup string // SQL run after the create is pushed
	}{
		{"create logged by event id only, uid column empty",
			`UPDATE sync_events SET uid = NULL WHERE op_type = 'create_node' AND node_id = 'MTIX-1'`},
		{"create logged by uid only, under another event id",
			`UPDATE sync_events SET event_id = 'f0000000-0000-7000-8000-000000000001' WHERE op_type = 'create_node' AND node_id = 'MTIX-1'`},
		{"holder without a uid and no create",
			`DELETE FROM sync_events WHERE op_type = 'create_node'; UPDATE nodes SET uid = '' WHERE id = 'MTIX-1'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s, raw := mutationTestStore(t)
			mustCreateNode(t, s, "MTIX-1", "")
			markEventsPushed(t, raw)
			_, err := raw.Exec(tt.setup)
			require.NoError(t, err)

			require.NoError(t, applyEventsInTx(s, []*model.SyncEvent{foreignCreate(t, "MTIX-1", hubUID, "hub's task")}))

			require.Equal(t, "Test MTIX-1", nodeRow(t, raw, "MTIX-1")["title"], "first writer keeps the number")
			require.Equal(t, 1, countRows(t, raw, `SELECT COUNT(*) FROM nodes`), "nothing moved, nothing added")
		})
	}
}

// TestApply_HubCreateOnNumberOfUnloggedLocalTask_CounterBehind_TakesFreeNumber:
// the sibling counter is behind the numbers in use, as after an import: the
// local task still lands on a free number.
func TestApply_HubCreateOnNumberOfUnloggedLocalTask_CounterBehind_TakesFreeNumber(t *testing.T) {
	fx, uids := unloggedFamily(t)
	mustCreateNode(t, fx.s, "MTIX-3", "")
	_, err := fx.raw.Exec(`DELETE FROM sync_events WHERE op_type = 'create_node'`)
	require.NoError(t, err)
	_, err = fx.raw.Exec(`UPDATE sequences SET value = 1 WHERE key = 'MTIX:'`)
	require.NoError(t, err)

	require.NoError(t, applyEventsInTx(fx.s, []*model.SyncEvent{foreignCreate(t, "MTIX-1", hubUID, "hub's task")}))

	require.Equal(t, "MTIX-4", rowByUID(t, fx.raw, uids["MTIX-1"])["id"], "past MTIX-2 and MTIX-3, which are held")
	require.Equal(t, "MTIX-1", rowByUID(t, fx.raw, hubUID)["id"])
	require.Equal(t, 5, countRows(t, fx.raw, `SELECT COUNT(*) FROM nodes`))
}

// TestApply_HubCreateOnNumberOfUnloggedLocalTask_RecordsMoveInHistory: applied
// through the Store-bound applier, the move is a display_path change of origin
// hub on each moved task, and the history keyed by the number of a task with
// no uid follows its new number.
func TestApply_HubCreateOnNumberOfUnloggedLocalTask_RecordsMoveInHistory(t *testing.T) {
	fx, uids := unloggedFamily(t)
	ctx := context.Background()
	_, err := fx.raw.Exec(`UPDATE nodes SET uid = '' WHERE id = 'MTIX-1.1'`)
	require.NoError(t, err)
	_, err = fx.raw.Exec(`INSERT INTO task_history (entry_id, task_uid, ts, actor, origin, kind, field)
		VALUES ('01a0f66f-0000-7000-8000-00000000aaaa', 'MTIX-1.1', '2026-01-01T00:00:00.000Z', 'a', 'local', 'change', 'title')`)
	require.NoError(t, err)

	require.NoError(t, applyBound(t, fx.s, foreignCreate(t, "MTIX-1", hubUID, "hub's task")))

	moved, err := fx.s.ListTaskHistory(ctx, "MTIX-3", 50)
	require.NoError(t, err)
	var seen int
	for _, h := range moved {
		if h.Field != "display_path" {
			continue
		}
		seen++
		require.Equal(t, model.HistoryOriginHub, h.Origin)
		require.NotNil(t, h.Old)
		require.NotNil(t, h.New)
		require.JSONEq(t, `"MTIX-1"`, *h.Old)
		require.JSONEq(t, `"MTIX-3"`, *h.New)
	}
	require.Equal(t, 1, seen, "one display_path entry for the moved task, from MTIX-1 to MTIX-3")
	require.Equal(t, uids["MTIX-1"], moved[0].TaskUID)

	require.Zero(t, countRows(t, fx.raw, `SELECT COUNT(*) FROM task_history WHERE task_uid = 'MTIX-1.1'`))
	require.Equal(t, 1, countRows(t, fx.raw, `SELECT COUNT(*) FROM task_history WHERE task_uid = 'MTIX-3.1' AND field = 'title'`),
		"the history of a task without a uid follows its number")
}

// TestRenumberForHubRejection_PushedCreate_IsPendingUnderNewNumber: a creation
// the hub rejected after it was marked pushed is sent again, under the new
// number.
func TestRenumberForHubRejection_PushedCreate_IsPendingUnderNewNumber(t *testing.T) {
	s, raw := replicaWithPendingNode(t)
	uid := nodeRow(t, raw, "MTIX-1")["uid"]
	markEventsPushed(t, raw)

	newID, err := s.RenumberForHubRejection(context.Background(), uid)
	require.NoError(t, err)

	require.Equal(t, 1, countRows(t, raw,
		`SELECT COUNT(*) FROM sync_events WHERE op_type = 'create_node' AND uid = ? AND node_id = ? AND sync_status = 'pending'`,
		uid, newID), "the rejected creation is pending under the new number")
}

// TestPullMovesSince_ListsOnlyHubMovesAfterTheMark: the pull lists the moves a
// replayed hub create made after the mark, each with the title; a local
// renumber and a move before the mark are not listed.
func TestPullMovesSince_ListsOnlyHubMovesAfterTheMark(t *testing.T) {
	fx, uids := unloggedFamily(t)
	ctx := context.Background()
	mark, err := fx.s.PullMoveMark(ctx)
	require.NoError(t, err)

	_, err = fx.s.RenumberForHubRejection(ctx, uids["MTIX-2"]) // a local move, origin system
	require.NoError(t, err)
	local, err := fx.s.PullMovesSince(ctx, mark)
	require.NoError(t, err)
	require.Empty(t, local, "a local renumber is not a move by a pull")

	require.NoError(t, applyBound(t, fx.s, foreignCreate(t, "MTIX-1", hubUID, "hub's task")))
	moves, err := fx.s.PullMovesSince(ctx, mark)
	require.NoError(t, err)
	require.Equal(t, []sqlite.PullMove{
		{OldID: "MTIX-1", NewID: "MTIX-4", Title: "Test MTIX-1", UID: uids["MTIX-1"]},
		{OldID: "MTIX-1.1", NewID: "MTIX-4.1", Title: "Test MTIX-1.1", UID: uids["MTIX-1.1"]},
	}, moves, "each moved task, the child included, with the number it holds now")

	after, err := fx.s.PullMoveMark(ctx)
	require.NoError(t, err)
	none, err := fx.s.PullMovesSince(ctx, after)
	require.NoError(t, err)
	require.Empty(t, none, "nothing after the mark")
}

// TestPullMovesSince_UidlessMovedChild_ListsItWithItsTitle: a moved task that
// has no uid is found by its number, and its title is listed.
func TestPullMovesSince_UidlessMovedChild_ListsItWithItsTitle(t *testing.T) {
	fx, _ := unloggedFamily(t)
	ctx := context.Background()
	_, err := fx.raw.Exec(`UPDATE nodes SET uid = '' WHERE id = 'MTIX-1.1'`)
	require.NoError(t, err)
	mark, err := fx.s.PullMoveMark(ctx)
	require.NoError(t, err)

	require.NoError(t, applyBound(t, fx.s, foreignCreate(t, "MTIX-1", hubUID, "hub's task")))

	moves, err := fx.s.PullMovesSince(ctx, mark)
	require.NoError(t, err)
	require.Contains(t, moves, sqlite.PullMove{OldID: "MTIX-1.1", NewID: "MTIX-3.1", Title: "Test MTIX-1.1"},
		"the task without a uid is listed with its title")
}
