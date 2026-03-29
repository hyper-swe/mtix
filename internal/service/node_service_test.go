// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// fixedClock returns a clock function that always returns the given time.
func fixedClock(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

// newTestNodeService creates a NodeService backed by a real SQLite store for integration tests.
func newTestNodeService(t *testing.T) (*service.NodeService, *sqlite.Store, *recordingBroadcaster) {
	t.Helper()

	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bc := newRecordingBroadcaster()
	cfg := &service.StaticConfig{AutoClaimEnabled: false}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	clock := fixedClock(now)
	logger := slog.Default()

	svc := service.NewNodeService(s, bc, cfg, logger, clock)
	return svc, s, bc
}

// TestNodeService_NewNodeService_NilStore_Panics verifies constructor rejects nil store.
func TestNodeService_NewNodeService_NilStore_Panics(t *testing.T) {
	assert.Panics(t, func() {
		service.NewNodeService(
			nil,
			&service.NoopBroadcaster{},
			&service.StaticConfig{},
			slog.Default(),
			func() time.Time { return time.Now() },
		)
	})
}

// TestNodeService_NewNodeService_NilClock_Panics verifies constructor rejects nil clock.
func TestNodeService_NewNodeService_NilClock_Panics(t *testing.T) {
	assert.Panics(t, func() {
		service.NewNodeService(
			&mockStore{},
			&service.NoopBroadcaster{},
			&service.StaticConfig{},
			slog.Default(),
			nil,
		)
	})
}

// TestNodeService_NewNodeService_NilBroadcasterConfigLogger_UsesDefaults verifies nil fallbacks.
func TestNodeService_NewNodeService_NilBroadcasterConfigLogger_UsesDefaults(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	// All optional deps nil — should use defaults.
	svc := service.NewNodeService(s, nil, nil, nil, fixedClock(now))
	require.NotNil(t, svc)

	// Verify it works with defaults.
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ", Title: "Nil Deps Test", Creator: "admin",
	})
	require.NoError(t, err)
	assert.NotEmpty(t, node.ID)
}

// TestNodeService_CreateNode_MissingProject_ReturnsError verifies project validation.
func TestNodeService_CreateNode_MissingProject_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	_, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Title:   "No Project",
		Creator: "admin",
	})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestNodeService_CreateNode_DescriptionTooLarge_ReturnsError verifies size limits.
func TestNodeService_CreateNode_DescriptionTooLarge_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	_, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project:     "PROJ",
		Title:       "Big Desc",
		Description: strings.Repeat("x", model.MaxDescriptionSize+1),
		Creator:     "admin",
	})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestNodeService_CreateNode_PromptTooLarge_ReturnsError verifies prompt size limit.
func TestNodeService_CreateNode_PromptTooLarge_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	_, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Big Prompt",
		Prompt:  strings.Repeat("x", model.MaxPromptSize+1),
		Creator: "admin",
	})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestNodeService_CreateNode_WithDeferUntil_SetsField verifies deferred creation.
func TestNodeService_CreateNode_WithDeferUntil_SetsField(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	deferTime := time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC)
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project:    "PROJ",
		Title:      "Deferred Node",
		Creator:    "admin",
		DeferUntil: &deferTime,
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	require.NotNil(t, got.DeferUntil)
	assert.Equal(t, deferTime.UTC().Format(time.RFC3339), got.DeferUntil.UTC().Format(time.RFC3339))
}

// TestNodeService_DeleteNode_NonExistentNode_ReturnsError verifies error propagation.
func TestNodeService_DeleteNode_NonExistentNode_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	err := svc.DeleteNode(ctx, "NONEXISTENT", false, "admin")
	assert.Error(t, err)
}

// TestNodeService_TransitionStatus_NonExistentNode_ReturnsError verifies error.
func TestNodeService_TransitionStatus_NonExistentNode_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	err := svc.TransitionStatus(ctx, "NONEXISTENT", model.StatusInProgress, "test", "admin")
	assert.Error(t, err)
}

// TestNodeService_CreateNode_ValidInput_DelegatesToStore verifies basic node creation.
func TestNodeService_CreateNode_ValidInput_DelegatesToStore(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	req := &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Test Node",
		Creator: "human@example.com",
	}

	node, err := svc.CreateNode(ctx, req)
	require.NoError(t, err)
	assert.Equal(t, "PROJ-1", node.ID)
	assert.Equal(t, "Test Node", node.Title)
	assert.Equal(t, model.StatusOpen, node.Status)
	assert.Equal(t, model.PriorityMedium, node.Priority)

	// Verify persisted in store.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, "Test Node", got.Title)
}

// TestNodeService_CreateNode_InvalidTitle_ReturnsError verifies title validation.
func TestNodeService_CreateNode_InvalidTitle_ReturnsError(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		title string
	}{
		{"empty title", ""},
		{"title too long", strings.Repeat("x", model.MaxTitleLength+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := &service.CreateNodeRequest{
				Project: "PROJ",
				Title:   tt.title,
				Creator: "agent-1",
			}
			_, err := svc.CreateNode(ctx, req)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

// TestNodeService_CreateNode_BroadcastsEvent verifies event broadcast after creation.
func TestNodeService_CreateNode_BroadcastsEvent(t *testing.T) {
	svc, _, bc := newTestNodeService(t)
	ctx := context.Background()

	req := &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Event Test",
		Creator: "agent-1",
	}

	_, err := svc.CreateNode(ctx, req)
	require.NoError(t, err)

	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventNodeCreated, events[0].Type)
	assert.Equal(t, "PROJ-1", events[0].NodeID)
}

// TestNodeService_CreateNode_AutoClaim_WhenConfigured verifies FR-11.2a auto-claim.
func TestNodeService_CreateNode_AutoClaim_WhenConfigured(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	bc := newRecordingBroadcaster()
	cfg := &service.StaticConfig{AutoClaimEnabled: true}
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	svc := service.NewNodeService(s, bc, cfg, slog.Default(), fixedClock(now))
	ctx := context.Background()

	// Create parent and claim it.
	parentReq := &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "agent-1",
	}
	parent, err := svc.CreateNode(ctx, parentReq)
	require.NoError(t, err)

	require.NoError(t, s.ClaimNode(ctx, parent.ID, "agent-1"))

	// Create child — should be auto-claimed by agent-1.
	bc.Reset()
	childReq := &service.CreateNodeRequest{
		ParentID: parent.ID,
		Project:  "PROJ",
		Title:    "Child",
		Creator:  "agent-1",
	}

	child, err := svc.CreateNode(ctx, childReq)
	require.NoError(t, err)

	// Verify child is in_progress and assigned to agent-1.
	got, err := s.GetNode(ctx, child.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusInProgress, got.Status)
	assert.Equal(t, "agent-1", got.Assignee)
}

// TestNodeService_CreateNode_ChildUnderTerminalParent_ReturnsError verifies FR-3.9.
func TestNodeService_CreateNode_ChildUnderTerminalParent_ReturnsError(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	parent, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Parent",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Transition parent to done.
	require.NoError(t, s.ClaimNode(ctx, parent.ID, "agent-1"))
	require.NoError(t, s.TransitionStatus(ctx, parent.ID, model.StatusDone, "complete", "agent-1"))

	// Attempt to create child under done parent.
	_, err = svc.CreateNode(ctx, &service.CreateNodeRequest{
		ParentID: parent.ID,
		Project:  "PROJ",
		Title:    "Child Under Done",
		Creator:  "admin",
	})
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "done")
}

// TestNodeService_UpdateNode_EnforcesStateMachine verifies updates validate transitions.
func TestNodeService_UpdateNode_EnforcesStateMachine(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "SM Test",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Valid update: change title.
	err = svc.UpdateNode(ctx, node.ID, &store.NodeUpdate{
		Title: strPtr("New Title"),
	})
	assert.NoError(t, err)

	// Verify title changed.
	got, err := svc.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "New Title", got.Title)
}

// TestNodeService_UsesInjectedClock verifies all timestamps use injected clock.
func TestNodeService_UsesInjectedClock(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	fixedTime := time.Date(2026, 6, 15, 10, 30, 0, 0, time.UTC)
	svc := service.NewNodeService(
		s,
		&service.NoopBroadcaster{},
		&service.StaticConfig{},
		slog.Default(),
		fixedClock(fixedTime),
	)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Clock Test",
		Creator: "admin",
	})
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, fixedTime.Format(time.RFC3339), got.CreatedAt.Format(time.RFC3339))
}

// TestNodeService_GetNode_NotFound verifies ErrNotFound propagation.
func TestNodeService_GetNode_NotFound(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	_, err := svc.GetNode(ctx, "NONEXISTENT")
	assert.ErrorIs(t, err, model.ErrNotFound)
}

// TestNodeService_DeleteNode_SoftDeletes verifies soft-delete via service.
func TestNodeService_DeleteNode_SoftDeletes(t *testing.T) {
	svc, s, bc := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "To Delete",
		Creator: "admin",
	})
	require.NoError(t, err)

	bc.Reset()
	err = svc.DeleteNode(ctx, node.ID, true, "admin")
	require.NoError(t, err)

	// Should be not found now.
	_, err = s.GetNode(ctx, node.ID)
	assert.ErrorIs(t, err, model.ErrNotFound)

	// Event broadcast.
	events := bc.Events()
	require.Len(t, events, 1)
	assert.Equal(t, service.EventNodeDeleted, events[0].Type)
}

// TestNodeService_TransitionStatus_ValidTransition verifies status transition.
func TestNodeService_TransitionStatus_ValidTransition(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Transition Test",
		Creator: "admin",
	})
	require.NoError(t, err)

	// open → deferred is valid.
	err = svc.TransitionStatus(ctx, node.ID, model.StatusDeferred, "waiting", "admin")
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, got.Status)
}

// TestNodeService_TransitionStatus_InvalidTransition verifies rejection.
func TestNodeService_TransitionStatus_InvalidTransition(t *testing.T) {
	svc, _, _ := newTestNodeService(t)
	ctx := context.Background()

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Invalid Transition",
		Creator: "admin",
	})
	require.NoError(t, err)

	// open → done is not valid (must go through in_progress first).
	err = svc.TransitionStatus(ctx, node.ID, model.StatusDone, "shortcut", "admin")
	assert.ErrorIs(t, err, model.ErrInvalidTransition)
}

// TestNodeService_CreateNode_DepthWarning verifies FR-1.1a advisory warning.
func TestNodeService_CreateNode_DepthWarning(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	// Create root node.
	root, err := svc.CreateNode(ctx, &service.CreateNodeRequest{
		Project: "PROJ",
		Title:   "Root",
		Creator: "admin",
	})
	require.NoError(t, err)

	// Verify depth 0.
	got, err := s.GetNode(ctx, root.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, got.Depth)
}

// TestNodeService_CreateNode_WithAllFields verifies full field population.
func TestNodeService_CreateNode_WithAllFields(t *testing.T) {
	svc, s, _ := newTestNodeService(t)
	ctx := context.Background()

	req := &service.CreateNodeRequest{
		Project:     "PROJ",
		Title:       "Full Node",
		Description: "A complete description",
		Prompt:      "Do this task",
		Acceptance:  "Task is done when...",
		Labels:      []string{"urgent", "backend"},
		Priority:    model.PriorityHigh,
		Creator:     "human@test.com",
	}

	node, err := svc.CreateNode(ctx, req)
	require.NoError(t, err)

	got, err := s.GetNode(ctx, node.ID)
	require.NoError(t, err)
	assert.Equal(t, "Full Node", got.Title)
	assert.Equal(t, "A complete description", got.Description)
	assert.Equal(t, "Do this task", got.Prompt)
	assert.Equal(t, "Task is done when...", got.Acceptance)
	assert.Equal(t, model.PriorityHigh, got.Priority)
	assert.Contains(t, got.Labels, "urgent")
	assert.Contains(t, got.Labels, "backend")
	assert.NotEmpty(t, got.ContentHash)
}

// strPtr returns a pointer to the given string.
func strPtr(s string) *string {
	return &s
}

// mockStore is a minimal mock for constructor tests that don't need real storage.
type mockStore struct{}

func (m *mockStore) CreateNode(_ context.Context, _ *model.Node) error { return nil }
func (m *mockStore) GetNode(_ context.Context, _ string) (*model.Node, error) {
	return nil, model.ErrNotFound
}
func (m *mockStore) UpdateNode(_ context.Context, _ string, _ *store.NodeUpdate) error { return nil }
func (m *mockStore) DeleteNode(_ context.Context, _ string, _ bool, _ string) error   { return nil }
func (m *mockStore) UndeleteNode(_ context.Context, _ string) error                    { return nil }
func (m *mockStore) ListNodes(_ context.Context, _ store.NodeFilter, _ store.ListOptions) ([]*model.Node, int, error) {
	return nil, 0, nil
}
func (m *mockStore) NextSequence(_ context.Context, _ string) (int, error) { return 1, nil }
func (m *mockStore) AddDependency(_ context.Context, _ *model.Dependency) error {
	return nil
}
func (m *mockStore) RemoveDependency(_ context.Context, _, _ string, _ model.DepType) error {
	return nil
}
func (m *mockStore) GetBlockers(_ context.Context, _ string) ([]*model.Dependency, error) {
	return nil, nil
}
func (m *mockStore) TransitionStatus(_ context.Context, _ string, _ model.Status, _, _ string) error {
	return nil
}
func (m *mockStore) ClaimNode(_ context.Context, _, _ string) error   { return nil }
func (m *mockStore) UnclaimNode(_ context.Context, _, _, _ string) error { return nil }
func (m *mockStore) ForceReclaimNode(_ context.Context, _, _ string, _ time.Duration) error {
	return nil
}
func (m *mockStore) CancelNode(_ context.Context, _, _, _ string, _ bool) error { return nil }
func (m *mockStore) UpdateProgress(_ context.Context, _ string, _ float64) error { return nil }
func (m *mockStore) GetDirectChildren(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *mockStore) Query(_ context.Context, _ string, _ ...any) (*sql.Rows, error) {
	return nil, nil
}
func (m *mockStore) QueryRow(_ context.Context, _ string, _ ...any) *sql.Row {
	return nil
}
func (m *mockStore) WriteDB() *sql.DB { return nil }
func (m *mockStore) GetAncestorChain(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *mockStore) GetSiblings(_ context.Context, _ string) ([]*model.Node, error) {
	return nil, nil
}
func (m *mockStore) SetAnnotations(_ context.Context, _ string, _ []model.Annotation) error {
	return nil
}
func (m *mockStore) SearchNodes(_ context.Context, _ string, _ store.NodeFilter, _ store.ListOptions) ([]*model.Node, int, error) {
	return nil, 0, nil
}
func (m *mockStore) GetTree(_ context.Context, _ string, _ int) ([]*model.Node, error) {
	return nil, nil
}
func (m *mockStore) GetStats(_ context.Context, _ string) (*store.Stats, error) {
	return &store.Stats{ByStatus: map[string]int{}, ByPriority: map[string]int{}, ByType: map[string]int{}}, nil
}
func (m *mockStore) GetActivity(_ context.Context, _ string, _, _ int) ([]model.ActivityEntry, error) {
	return nil, nil
}
func (m *mockStore) Close() error { return nil }
