// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-44: a blocker resolved on another client and applied via the SYNC path
// must auto-unblock local dependents, exactly as the local transition path
// does. applyTransitionStatus previously only UPDATE'd the node's status and
// skipped the unblockDependents/recalculateProgress recompute that
// transition.go runs, leaving dependents sticky-blocked in team/distributed
// setups. White-box so it can drive the sync-apply dispatch directly.
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

// mkUnblockRoot builds a minimal valid root node (mirrors makeRootNode from the
// black-box tests, inlined for this white-box package).
func mkUnblockRoot(id, project, title string, now time.Time) *model.Node {
	return &model.Node{
		ID: id, Project: project, Depth: 0, Seq: 1, Title: title,
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType:    model.NodeTypeStory,
		Creator:     "test",
		ContentHash: model.ComputeContentHash(title, "", "", "", nil),
		CreatedAt:   now, UpdatedAt: now,
	}
}

func TestApplyTransitionStatus_ResolvingBlocker_AutoUnblocksDependents(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-1", "PROJ", "Blocker", now)))
	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-2", "PROJ", "Dependent", now)))

	// PROJ-1 blocks PROJ-2 -> PROJ-2 is auto-blocked.
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "PROJ-1", ToID: "PROJ-2", DepType: model.DepTypeBlocks,
		CreatedAt: now, CreatedBy: "pm",
	}))
	b, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	require.Equal(t, model.StatusBlocked, b.Status, "precondition: PROJ-2 blocked on PROJ-1")

	// Simulate PROJ-1 resolved on ANOTHER client and synced in: apply a
	// transition_status(PROJ-1 -> done) through the sync-apply dispatch, NOT
	// the local transition path.
	payload, err := model.EncodePayload(&model.TransitionStatusPayload{
		From: model.StatusOpen, To: model.StatusDone,
	})
	require.NoError(t, err)
	event := &model.SyncEvent{
		EventID: "evt-mtix44", NodeID: "PROJ-1", OpType: model.OpTransitionStatus,
		Payload: payload, AuthorID: "remote",
	}
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return dispatchApply(ctx, tx, event)
	}))

	// PROJ-1 is done, and PROJ-2 must be auto-unblocked.
	a, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.Equal(t, model.StatusDone, a.Status)

	b2, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	require.NotEqual(t, model.StatusBlocked, b2.Status,
		"MTIX-44: a sync-applied blocker resolution must auto-unblock its dependents")
}

// TestRefreshBlocked_RecoversStickyBlockedNode: the manual escape hatch clears a
// node left sticky-blocked (its blocker resolved without a recompute).
func TestRefreshBlocked_RecoversStickyBlockedNode(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-1", "PROJ", "Blocker", now)))
	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-2", "PROJ", "Dependent", now)))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "PROJ-1", ToID: "PROJ-2", DepType: model.DepTypeBlocks, CreatedAt: now, CreatedBy: "pm",
	}))

	// Simulate the pre-fix drift: resolve the blocker's status directly,
	// bypassing the auto-unblock, so PROJ-2 is left sticky-blocked.
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(ctx, `UPDATE nodes SET status = 'done' WHERE id = 'PROJ-1'`)
		return e
	}))
	stuck, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	require.Equal(t, model.StatusBlocked, stuck.Status, "precondition: PROJ-2 sticky-blocked")

	require.NoError(t, s.RefreshBlocked(ctx, "PROJ-2"))
	got, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	require.NotEqual(t, model.StatusBlocked, got.Status, "RefreshBlocked must clear a resolved sticky block")
}

// TestRefreshBlocked_NoopWhenBlockerUnresolved: it never overrides a genuine block.
func TestRefreshBlocked_NoopWhenBlockerUnresolved(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-1", "PROJ", "Blocker", now)))
	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-2", "PROJ", "Dependent", now)))
	require.NoError(t, s.AddDependency(ctx, &model.Dependency{
		FromID: "PROJ-1", ToID: "PROJ-2", DepType: model.DepTypeBlocks, CreatedAt: now, CreatedBy: "pm",
	}))
	// PROJ-1 is still open (unresolved). RefreshBlocked must leave PROJ-2 blocked.
	require.NoError(t, s.RefreshBlocked(ctx, "PROJ-2"))
	got, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	require.Equal(t, model.StatusBlocked, got.Status, "RefreshBlocked must not override a genuine block")
}
