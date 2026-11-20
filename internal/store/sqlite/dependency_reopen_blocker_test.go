// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-112.17.16.40: the block rule is "a blocks-blocker is unresolved". A
// blocker that leaves done, cancelled or invalidated (reopen, restore, rerun,
// or a synced transition) must block its open dependents again (FR-3.8).

func newReopenStore(t *testing.T) (*Store, context.Context, time.Time) {
	t.Helper()
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-1", "PROJ", "Blocker", now)))
	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-2", "PROJ", "Dependent", now)))
	return s, ctx, now
}

func setStatusRaw(ctx context.Context, t *testing.T, s *Store, id string, st model.Status) {
	t.Helper()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE nodes SET status = ? WHERE id = ?`, string(st), id)
		return e
	}))
}

func linkBlocks(ctx context.Context, t *testing.T, s *Store, now time.Time) {
	t.Helper()
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "PROJ-1", ToID: "PROJ-2", DepType: model.DepTypeBlocks, CreatedAt: now, CreatedBy: "pm",
	}))
}

func statusOf(ctx context.Context, t *testing.T, s *Store, id string) model.Status {
	t.Helper()
	n, err := s.GetNode(ctx, id)
	require.NoError(t, err)
	return n.Status
}

func TestTransitionStatus_ReopenResolvedBlocker_BlocksDependent(t *testing.T) {
	for _, resolved := range []model.Status{model.StatusDone, model.StatusCancelled} {
		t.Run(string(resolved), func(t *testing.T) {
			s, ctx, now := newReopenStore(t)
			setStatusRaw(ctx, t, s, "PROJ-1", resolved)
			linkBlocks(ctx, t, s, now)
			require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"), "precondition: link over resolved blocker")

			require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusOpen, "reopen", "pm"))
			require.Equal(t, model.StatusBlocked, statusOf(ctx, t, s, "PROJ-2"))

			// Resolving again unblocks it: the round trip is lossless.
			require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusCancelled, "again", "pm"))
			require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"))
		})
	}
}

func TestRestoreInvalidated_ResolvedBlocker_BlocksDependent(t *testing.T) {
	s, ctx, now := newReopenStore(t)
	setStatusRaw(ctx, t, s, "PROJ-1", model.StatusInvalidated)
	linkBlocks(ctx, t, s, now)
	require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"))

	_, err := s.RestoreInvalidated(ctx, "PROJ-1", "pm")
	require.NoError(t, err)
	require.Equal(t, model.StatusBlocked, statusOf(ctx, t, s, "PROJ-2"))
}

func TestTransitionStatus_ReopenBlocker_LeavesNonOpenDependentsAlone(t *testing.T) {
	s, ctx, now := newReopenStore(t)
	setStatusRaw(ctx, t, s, "PROJ-1", model.StatusDone)
	linkBlocks(ctx, t, s, now)
	setStatusRaw(ctx, t, s, "PROJ-2", model.StatusDone)

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusOpen, "reopen", "pm"))
	require.Equal(t, model.StatusDone, statusOf(ctx, t, s, "PROJ-2"), "a done dependent is never auto-blocked (FR-3.8)")
}

func TestApplyTransitionStatus_ReopenResolvedBlocker_BlocksDependent(t *testing.T) {
	for _, resolved := range []model.Status{model.StatusDone, model.StatusCancelled, model.StatusInvalidated} {
		t.Run(string(resolved), func(t *testing.T) {
			s, ctx, now := newReopenStore(t)
			markSynced(ctx, t, s)
			setStatusRaw(ctx, t, s, "PROJ-1", resolved)
			linkBlocks(ctx, t, s, now)
			require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"))

			applyOne(ctx, t, s, &model.SyncEvent{
				EventID: "evt-reopen", NodeID: "PROJ-1", OpType: model.OpTransitionStatus, AuthorID: "remote",
				Payload: mustEncode(t, &model.TransitionStatusPayload{From: resolved, To: model.StatusOpen}),
			})
			require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-1"))
			require.Equal(t, model.StatusBlocked, statusOf(ctx, t, s, "PROJ-2"))
		})
	}
}

// Other link types over a resolved blocker never count: a related or
// discovered_from edge from an open node must not block the dependent, and a
// blocks link over a resolved blocker leaves it open (kills the dep_type
// filter mutation in hasUnresolvedBlocker).
func TestAddDependency_BlocksOverResolvedBlocker_IgnoresOtherLinkTypes(t *testing.T) {
	for _, dt := range []model.DepType{model.DepTypeRelated, model.DepTypeDiscoveredFrom} {
		t.Run(string(dt), func(t *testing.T) {
			s, ctx, now := newReopenStore(t)
			require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-3", "PROJ", "Other", now)))
			require.NoError(t, s.AddDependency(ctx, &model.Dependency{
				FromID: "PROJ-3", ToID: "PROJ-2", DepType: dt, CreatedAt: now, CreatedBy: "pm",
			}))
			setStatusRaw(ctx, t, s, "PROJ-1", model.StatusDone)
			linkBlocks(ctx, t, s, now)
			require.Equal(t, model.StatusOpen, statusOf(ctx, t, s, "PROJ-2"))
		})
	}
}

// A failing blocker count surfaces as the wrapped count-blockers error and
// never silently skips the block (kills swallow and dropped-check mutations).
func TestAutoBlockNode_BlockerCountFails_ReturnsWrappedError(t *testing.T) {
	s, ctx, now := newReopenStore(t)
	linkBlocks(ctx, t, s, now)
	setStatusRaw(ctx, t, s, "PROJ-2", model.StatusOpen)
	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		// Break only the column the count reads; the status read still works.
		if _, e := tx.ExecContext(ctx, `ALTER TABLE dependencies RENAME COLUMN dep_type TO dep_type_x`); e != nil {
			return e
		}
		return autoBlockNode(ctx, tx, s, "PROJ-2")
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "count blockers of PROJ-2")
}
