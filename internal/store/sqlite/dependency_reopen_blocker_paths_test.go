// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-112.17.16.40: a reopened blocker re-blocks EVERY open or in-progress
// dependent, and each error on the re-block and unblock paths surfaces through
// the real entry points with nothing committed (FR-3.8).

const failBlockTrigger = `CREATE TRIGGER fail_block BEFORE UPDATE OF status ON nodes
	WHEN NEW.status = 'blocked' BEGIN SELECT RAISE(ABORT, 'injected block failure'); END`

// newFanoutStore returns a store with a done blocker PROJ-1 that blocks PROJ-2
// and PROJ-3 (open) and PROJ-4 (in_progress), linked over the resolved blocker.
func newFanoutStore(t *testing.T) (*Store, context.Context) {
	t.Helper()
	s, ctx, now := newReopenStore(t)
	for _, id := range []string{"PROJ-3", "PROJ-4"} {
		require.NoError(t, s.CreateNode(ctx, mkUnblockRoot(id, "PROJ", "Dependent "+id, now)))
	}
	setStatusRaw(ctx, t, s, "PROJ-1", model.StatusDone)
	setStatusRaw(ctx, t, s, "PROJ-4", model.StatusInProgress)
	for _, to := range []string{"PROJ-2", "PROJ-3", "PROJ-4"} {
		require.NoError(t, s.AddDependency(ctx, &model.Dependency{
			FromID: "PROJ-1", ToID: to, DepType: model.DepTypeBlocks, CreatedAt: now, CreatedBy: "pm",
		}))
	}
	for _, id := range []string{"PROJ-2", "PROJ-3"} {
		require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, id), "precondition: link over resolved blocker")
	}
	return s, ctx
}

func requireAllBlocked(ctx context.Context, t *testing.T, s *Store) {
	t.Helper()
	for _, id := range []string{"PROJ-2", "PROJ-3", "PROJ-4"} {
		require.Equal(t, model.StatusBlocked, statusOf(ctx, t, s, id), id)
	}
}

func TestTransitionStatus_ReopenBlockerWithSeveralDependents_BlocksAll(t *testing.T) {
	s, ctx := newFanoutStore(t)
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusOpen, "reopen", "pm"))
	requireAllBlocked(ctx, t, s)
}

func TestApplyTransitionStatus_ReopenBlockerWithSeveralDependents_BlocksAll(t *testing.T) {
	s, ctx := newFanoutStore(t)
	markSynced(ctx, t, s)
	applyOne(ctx, t, s, reopenEvent(t))
	require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-1"))
	requireAllBlocked(ctx, t, s)
}

func reopenEvent(t *testing.T) *model.SyncEvent {
	return &model.SyncEvent{
		EventID: "evt-reopen", NodeID: "PROJ-1", OpType: model.OpTransitionStatus, AuthorID: "remote",
		Payload: mustEncode(t, &model.TransitionStatusPayload{From: model.StatusDone, To: model.StatusOpen}),
	}
}

func execRaw(ctx context.Context, t *testing.T, s *Store, stmt string) {
	t.Helper()
	_, err := s.writeDB.ExecContext(ctx, stmt)
	require.NoError(t, err)
}

func TestTransitionStatus_ReopenBlockerWhenReblockFails_ReturnsErrorAndCommitsNothing(t *testing.T) {
	s, ctx := newFanoutStore(t)
	execRaw(ctx, t, s, failBlockTrigger)

	err := s.TransitionStatus(ctx, "PROJ-1", model.StatusOpen, "reopen", "pm")
	require.Error(t, err)
	require.Contains(t, err.Error(), "auto-block dependents of PROJ-1")
	require.Contains(t, err.Error(), "injected block failure")
	require.Equal(t, model.StatusDone, statusOf(ctx, t, s, "PROJ-1"), "the reopen is rolled back")
	require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"))
}

func TestApplyTransitionStatus_ReopenBlockerWhenReblockFails_ReturnsErrorAndCommitsNothing(t *testing.T) {
	s, ctx := newFanoutStore(t)
	markSynced(ctx, t, s)
	execRaw(ctx, t, s, failBlockTrigger)

	err := s.WithTx(ctx, func(tx *sql.Tx) error { return dispatchApply(ctx, tx, reopenEvent(t)) })
	require.Error(t, err)
	require.Contains(t, err.Error(), "auto-block dependents")
	require.Contains(t, err.Error(), "injected block failure")
	require.Equal(t, model.StatusDone, statusOf(ctx, t, s, "PROJ-1"), "the reopen is rolled back")
	require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"))
}

func TestApplyTransitionStatus_PriorStatusUnreadable_ReturnsWrappedError(t *testing.T) {
	s, ctx := newFanoutStore(t)
	markSynced(ctx, t, s)
	execRaw(ctx, t, s, `ALTER TABLE nodes RENAME COLUMN status TO status_unreadable`)

	err := s.WithTx(ctx, func(tx *sql.Tx) error { return dispatchApply(ctx, tx, reopenEvent(t)) })
	require.Error(t, err)
	require.Contains(t, err.Error(), "read prior status")
}

func TestRefreshBlocked_BlockerCountFails_ReturnsWrappedError(t *testing.T) {
	s, ctx, now := newReopenStore(t)
	linkBlocks(ctx, t, s, now)
	setStatusRaw(ctx, t, s, "PROJ-2", model.StatusBlocked)
	execRaw(ctx, t, s, `ALTER TABLE dependencies RENAME COLUMN dep_type TO dep_type_x`)

	err := s.RefreshBlocked(ctx, "PROJ-2")
	require.Error(t, err)
	require.Contains(t, err.Error(), "count blockers of PROJ-2")
	require.Equal(t, model.StatusBlocked, statusOf(ctx, t, s, "PROJ-2"), "nothing is unblocked on error")
}

func depRows(ctx context.Context, t *testing.T, s *Store) int {
	t.Helper()
	var n int
	require.NoError(t, s.writeDB.QueryRowContext(ctx, `SELECT COUNT(*) FROM dependencies`).Scan(&n))
	return n
}

func TestAddDependency_AutoBlockFails_ReturnsErrorAndCommitsNothing(t *testing.T) {
	s, ctx, now := newReopenStore(t)
	execRaw(ctx, t, s, failBlockTrigger)

	err := s.AddDependency(ctx, &model.Dependency{
		FromID: "PROJ-1", ToID: "PROJ-2", DepType: model.DepTypeBlocks, CreatedAt: now, CreatedBy: "pm",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "auto-block PROJ-2")
	require.Contains(t, err.Error(), "injected block failure")
	require.Equal(t, 0, depRows(ctx, t, s), "the edge is rolled back")
	require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"))
}

func TestApplyLinkDep_AutoBlockFails_ReturnsErrorAndCommitsNothing(t *testing.T) {
	s, ctx, _ := newReopenStore(t)
	markSynced(ctx, t, s)
	execRaw(ctx, t, s, failBlockTrigger)

	ev := &model.SyncEvent{
		EventID: "evt-link-fail", NodeID: "PROJ-1", OpType: model.OpLinkDep, AuthorID: "remote",
		Payload: mustEncode(t, &model.LinkDepPayload{DependsOnNodeID: "PROJ-2", DepType: string(model.DepTypeBlocks)}),
	}
	err := s.WithTx(ctx, func(tx *sql.Tx) error { return dispatchApply(ctx, tx, ev) })
	require.Error(t, err)
	require.Contains(t, err.Error(), "auto-block PROJ-2")
	require.Contains(t, err.Error(), "injected block failure")
	require.Equal(t, 0, depRows(ctx, t, s), "the edge is rolled back")
	require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"))
}
