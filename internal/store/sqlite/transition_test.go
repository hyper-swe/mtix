// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestTransition_OpenToInProgress_ViaClaim verifies open→in_progress.
func TestTransition_OpenToInProgress_ViaClaim(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Open Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.TransitionStatus(ctx, "PROJ-1", model.StatusInProgress, "", "agent-1")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

// TestTransition_InProgressToDone_ViaMarkDone verifies in_progress→done.
func TestTransition_InProgressToDone_ViaMarkDone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "In Progress Node", now)
	node.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "Work complete", "agent-1")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDone, got.Status)
	assert.Equal(t, 1.0, got.Progress)
}

// TestTransition_DoneToOpen_ViaReopen verifies done→open via reopen.
func TestTransition_DoneToOpen_ViaReopen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Done Node", now)
	node.Status = model.StatusDone
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.TransitionStatus(ctx, "PROJ-1", model.StatusOpen, "Needs more work", "pm-1")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestTransition_AllIdempotentCases_FR7_7a verifies same-status transitions are no-ops.
func TestTransition_AllIdempotentCases_FR7_7a(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	tests := []model.Status{
		model.StatusOpen,
		model.StatusInProgress,
		model.StatusDone,
		model.StatusDeferred,
		model.StatusCancelled,
	}

	for i, status := range tests {
		t.Run(string(status), func(t *testing.T) {
			id := "PROJ-" + itoa(i+1)
			node := makeRootNode(id, "PROJ", "Node "+string(status), now)
			node.Status = status
			require.NoError(t, s.CreateNode(ctx, node))

			// Idempotent transition: same status → same status = success.
			err := s.TransitionStatus(ctx, id, status, "", "agent-1")
			assert.NoError(t, err, "idempotent transition for %s should succeed", status)
		})
	}
}

// TestTransition_SetsClosedAt_OnDoneOrCancelled verifies closed_at is set.
func TestTransition_SetsClosedAt_OnDoneOrCancelled(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Test done transition.
	node1 := makeRootNode("PROJ-1", "PROJ", "Done Node", now)
	node1.Status = model.StatusInProgress
	require.NoError(t, s.CreateNode(ctx, node1))

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "Done", "agent-1"))
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.NotNil(t, got.ClosedAt, "closed_at should be set for done status")

	// Test cancelled transition.
	node2 := makeRootNode("PROJ-2", "PROJ", "Cancelled Node", now)
	require.NoError(t, s.CreateNode(ctx, node2))

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-2", model.StatusCancelled, "No longer needed", "pm-1"))
	got, err = s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.NotNil(t, got.ClosedAt, "closed_at should be set for cancelled status")
}

// TestTransition_ClearsClosedAt_OnReopen verifies closed_at is cleared.
func TestTransition_ClearsClosedAt_OnReopen(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Reopen Node", now)
	node.Status = model.StatusDone
	closedAt := now.Add(-time.Hour)
	node.ClosedAt = &closedAt
	require.NoError(t, s.CreateNode(ctx, node))

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusOpen, "Reopening", "pm-1"))

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Nil(t, got.ClosedAt, "closed_at should be cleared on reopen")
}

// TestTransition_RecordsActivityEntry verifies activity is recorded.
func TestTransition_RecordsActivityEntry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Activity Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusInProgress, "Starting work", "agent-1"))

	var activityJSON string
	err := s.QueryRow(ctx, "SELECT activity FROM nodes WHERE id = ?", "PROJ-1").Scan(&activityJSON)
	require.NoError(t, err)
	assert.Contains(t, activityJSON, "status_change")
	assert.Contains(t, activityJSON, "from_status")
	assert.Contains(t, activityJSON, "agent-1")
}

// TestTransition_InvalidTransition_Rejected verifies invalid transitions.
func TestTransition_InvalidTransition_Rejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	tests := []struct {
		name string
		from model.Status
		to   model.Status
	}{
		{"open→done", model.StatusOpen, model.StatusDone},
		{"done→in_progress", model.StatusDone, model.StatusInProgress},
		{"cancelled→done", model.StatusCancelled, model.StatusDone},
		{"invalidated→done", model.StatusInvalidated, model.StatusDone},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id := "PROJ-" + itoa(i+20)
			node := makeRootNode(id, "PROJ", "Node "+tt.name, now)
			node.Status = tt.from
			require.NoError(t, s.CreateNode(ctx, node))

			err := s.TransitionStatus(ctx, id, tt.to, "Reason", "agent-1")
			assert.ErrorIs(t, err, model.ErrInvalidTransition)
		})
	}
}

// TestTransition_DeferIdempotent_SameUntil verifies FR-7.7a for deferred.
func TestTransition_DeferIdempotent_SameUntil(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Defer Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Defer the node.
	err := s.TransitionStatus(ctx, "PROJ-1", model.StatusDeferred, "Postponed", "pm-1")
	require.NoError(t, err)

	// Same transition again = idempotent success.
	err = s.TransitionStatus(ctx, "PROJ-1", model.StatusDeferred, "Postponed again", "pm-1")
	assert.NoError(t, err, "idempotent deferred transition should succeed")
}

// TestTransition_DeferNotIdempotent_DifferentUntil:
// This is handled at the service layer since defer_until isn't part of
// TransitionStatus. At the store level, same-status = idempotent.
func TestTransition_DeferNotIdempotent_DifferentUntil(t *testing.T) {
	// Placeholder — service layer will differentiate by defer_until value.
	t.Skip("defer_until differentiation is a service-layer concern")
}

// TestTransition_NonExistent_ReturnsNotFound verifies ErrNotFound for missing node.
func TestTransition_NonExistent_ReturnsNotFound(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	err := s.TransitionStatus(ctx, "NONEXISTENT", model.StatusDone, "done", "agent-1")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestTransition_SavesPreviousStatus_OnBlocked verifies previous_status save.
func TestTransition_SavesPreviousStatus_OnBlocked(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Block target", now)
	node.Status = model.StatusInProgress
	node.Assignee = "agent-001"
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.TransitionStatus(ctx, "PROJ-1", model.StatusBlocked, "Blocked by dep", "system")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusBlocked, got.Status)
	assert.Equal(t, model.StatusInProgress, got.PreviousStatus)
}

// TestTransition_SavesPreviousStatus_OnInvalidated verifies previous_status save.
func TestTransition_SavesPreviousStatus_OnInvalidated(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Invalidate target", now)
	require.NoError(t, s.CreateNode(ctx, node))

	err := s.TransitionStatus(ctx, "PROJ-1", model.StatusInvalidated, "Parent rerun", "system")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusInvalidated, got.Status)
	assert.Equal(t, model.StatusOpen, got.PreviousStatus)
}

// TestTransition_RecalculatesParentProgress_OnDone verifies FR-5.7.
func TestTransition_RecalculatesParentProgress_OnDone(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child 1", 1, 1, now)
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Child 2", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	// Transition child1 to in_progress, then done.
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1", model.StatusInProgress, "started", "agent"))
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1.1", model.StatusDone, "done", "agent"))

	// Parent should have progress 0.5 (1 done, 1 open).
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.InDelta(t, 0.5, got.Progress, 0.01)
}
