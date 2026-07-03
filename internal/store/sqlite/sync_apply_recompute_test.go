// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-44 (GA-robust follow-up): the sync-apply handlers must run the same
// derived-state recomputes their local-path counterparts run. These white-box
// tests drive dispatchApply directly and pin the previously-missing recomputes:
//   - applyLinkDep   auto-blocks the dependent   (mirrors AddDependency)
//   - applyUnlinkDep auto-unblocks the dependent (mirrors RemoveDependency)
//   - applyCreateNode rolls up parent progress   (mirrors CreateNode, FR-5.7)
//   - applyDelete     rolls up parent progress   (mirrors DeleteNode, FR-5.7)
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

// applyOne dispatches a single event through the sync-apply handler under test,
// inside its own tx (mirrors the pattern in sync_apply_unblock_test.go).
func applyOne(ctx context.Context, t *testing.T, s *Store, e *model.SyncEvent) {
	t.Helper()
	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error {
		return dispatchApply(ctx, tx, e)
	}))
}

// TestApplyLinkDep_AutoBlocksDependent: a synced `blocks` edge must auto-block
// its dependent, exactly as the local AddDependency path does.
func TestApplyLinkDep_AutoBlocksDependent(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-1", "PROJ", "Blocker", now)))
	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-2", "PROJ", "Dependent", now)))

	// Simulate a dependency created on ANOTHER client and synced in: PROJ-1
	// blocks PROJ-2. The event's NodeID is the edge's from-node (blocker);
	// the payload's DependsOnNodeID is the to-node (dependent) — the node the
	// local AddDependency passes to autoBlockNode.
	event := &model.SyncEvent{
		EventID: "evt-link", NodeID: "PROJ-1", OpType: model.OpLinkDep, AuthorID: "remote",
		Payload: mustEncode(t, &model.LinkDepPayload{
			DependsOnNodeID: "PROJ-2", DepType: string(model.DepTypeBlocks),
		}),
	}
	applyOne(ctx, t, s, event)

	dep, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	require.Equal(t, model.StatusBlocked, dep.Status,
		"MTIX-44: a sync-applied blocks edge must auto-block its dependent")
}

// TestApplyUnlinkDep_LastBlocker_AutoUnblocksDependent: removing the last
// `blocks` edge via sync must auto-unblock the dependent, as RemoveDependency
// does locally.
func TestApplyUnlinkDep_LastBlocker_AutoUnblocksDependent(t *testing.T) {
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
	pre, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	require.Equal(t, model.StatusBlocked, pre.Status, "precondition: PROJ-2 blocked")

	// Simulate the dependency being removed on ANOTHER client and synced in.
	event := &model.SyncEvent{
		EventID: "evt-unlink", NodeID: "PROJ-1", OpType: model.OpUnlinkDep, AuthorID: "remote",
		Payload: mustEncode(t, &model.UnlinkDepPayload{
			DependsOnNodeID: "PROJ-2", DepType: string(model.DepTypeBlocks),
		}),
	}
	applyOne(ctx, t, s, event)

	dep, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	require.NotEqual(t, model.StatusBlocked, dep.Status,
		"MTIX-44: removing the last blocks edge via sync must auto-unblock the dependent")
}

// TestApplyCreateNode_RecomputesParentProgress: a child created via sync must
// roll its (progress 0.0) weight into the parent's average, as CreateNode does
// locally (FR-5.7). Without the recompute the parent's progress stays stale.
func TestApplyCreateNode_RecomputesParentProgress(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-1", "PROJ", "Parent", now)))

	// First child, synced in, then marked done -> parent progress = 1.0.
	applyOne(ctx, t, s, &model.SyncEvent{
		EventID: "evt-c1", NodeID: "PROJ-1.1", OpType: model.OpCreateNode, AuthorID: "remote",
		Payload: mustEncode(t, &model.CreateNodePayload{Title: "Child1", ParentID: "PROJ-1"}),
	})
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1", model.StatusInProgress, "start", "remote"))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1", model.StatusDone, "finish", "remote"))
	p1, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.InDelta(t, 1.0, p1.Progress, 1e-9, "precondition: single done child -> parent 1.0")

	// Second child, synced in at progress 0.0 -> parent must drop to 0.5.
	applyOne(ctx, t, s, &model.SyncEvent{
		EventID: "evt-c2", NodeID: "PROJ-1.2", OpType: model.OpCreateNode, AuthorID: "remote",
		Payload: mustEncode(t, &model.CreateNodePayload{Title: "Child2", ParentID: "PROJ-1"}),
	})
	p2, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.InDelta(t, 0.5, p2.Progress, 1e-9,
		"MTIX-44: a sync-created child must roll up into parent progress (0.5), not leave it stale (1.0)")
}

// TestApplyDelete_RecomputesParentProgress: a child deleted via sync must be
// excluded from the parent's denominator, as DeleteNode does locally (FR-5.7).
func TestApplyDelete_RecomputesParentProgress(t *testing.T) {
	s, err := New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	require.NoError(t, s.CreateNode(ctx, mkUnblockRoot("PROJ-1", "PROJ", "Parent", now)))

	// Two children: PROJ-1.1 (open, 0.0) and PROJ-1.2 (done, 1.0) -> parent 0.5.
	applyOne(ctx, t, s, &model.SyncEvent{
		EventID: "evt-d1", NodeID: "PROJ-1.1", OpType: model.OpCreateNode, AuthorID: "remote",
		Payload: mustEncode(t, &model.CreateNodePayload{Title: "Child1", ParentID: "PROJ-1"}),
	})
	applyOne(ctx, t, s, &model.SyncEvent{
		EventID: "evt-d2", NodeID: "PROJ-1.2", OpType: model.OpCreateNode, AuthorID: "remote",
		Payload: mustEncode(t, &model.CreateNodePayload{Title: "Child2", ParentID: "PROJ-1"}),
	})
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.2", model.StatusInProgress, "start", "remote"))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.2", model.StatusDone, "finish", "remote"))
	pre, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.InDelta(t, 0.5, pre.Progress, 1e-9, "precondition: one open + one done child -> parent 0.5")

	// Delete the open child via sync -> it drops out of the denominator ->
	// parent must recompute to 1.0 (only the done child remains).
	applyOne(ctx, t, s, &model.SyncEvent{
		EventID: "evt-del", NodeID: "PROJ-1.1", OpType: model.OpDelete, AuthorID: "remote",
		Payload: mustEncode(t, &model.DeletePayload{}),
	})
	post, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	require.InDelta(t, 1.0, post.Progress, 1e-9,
		"MTIX-44: a sync-deleted child must be excluded from parent progress (1.0), not leave it stale (0.5)")
}
