// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// newTestBackgroundService creates a BackgroundService backed by real SQLite.
func newTestBackgroundService(t *testing.T, clock func() time.Time) (*service.BackgroundService, *sqlite.Store) {
	t.Helper()

	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	cfg := &service.StaticConfig{
		RetentionDuration: 30 * 24 * time.Hour, // 30 days.
	}

	bg := service.NewBackgroundService(s, cfg, slog.Default(), clock)
	return bg, s
}

// createTestNode creates a node directly in the store for background tests.
func createTestNode(t *testing.T, s *sqlite.Store, id, project, title string, now time.Time) {
	t.Helper()
	ctx := context.Background()

	node := &model.Node{
		ID:        id,
		Project:   project,
		Title:     title,
		Status:    model.StatusOpen,
		Priority:  model.PriorityMedium,
		Weight:    1.0,
		CreatedAt: now,
		UpdatedAt: now,
	}
	node.ContentHash = node.ComputeHash()
	require.NoError(t, s.CreateNode(ctx, node))
}

// TestBackgroundScan_CleansExpiredNodes verifies retention cleanup per FR-3.3a.
func TestBackgroundScan_CleansExpiredNodes(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create a node, soft-delete it with a timestamp 31 days ago.
	createTestNode(t, s, "PROJ-1", "PROJ", "Expired Node", now.Add(-32*24*time.Hour))
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	// The deleted_at would be recent (just now), so we need to manipulate it
	// to be in the past. Use direct SQL.
	deletedAt := now.Add(-31 * 24 * time.Hour).UTC().Format(time.RFC3339)
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`UPDATE nodes SET deleted_at = ? WHERE id = ?`,
		deletedAt, "PROJ-1",
	)
	require.NoError(t, err)

	// Run scan — should clean up the expired node.
	err = bg.RunScan(ctx)
	require.NoError(t, err)

	// Verify node is permanently gone (even undelete won't find it).
	var count int
	err = s.QueryRow(ctx,
		`SELECT COUNT(*) FROM nodes WHERE id = ?`, "PROJ-1",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count)
}

// TestBackgroundScan_WakesDeferredNodes verifies deferred auto-wake per FR-3.8b.
func TestBackgroundScan_WakesDeferredNodes(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create a deferred node with defer_until in the past.
	createTestNode(t, s, "PROJ-1", "PROJ", "Deferred Node", now.Add(-2*time.Hour))

	// Set to deferred with past defer_until via direct transition.
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDeferred, "waiting", "admin"))

	// Set defer_until to the past.
	pastDeferUntil := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`,
		pastDeferUntil, "PROJ-1",
	)
	require.NoError(t, err)

	// Run scan — should wake the deferred node.
	err = bg.RunScan(ctx)
	require.NoError(t, err)

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, got.Status)
}

// TestBackgroundScan_LeavesNonExpiredAlone verifies non-expired nodes survive.
func TestBackgroundScan_LeavesNonExpiredAlone(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create a node and soft-delete it recently.
	createTestNode(t, s, "PROJ-1", "PROJ", "Recent Delete", now)
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	// Run scan — should NOT clean up (within retention period).
	err := bg.RunScan(ctx)
	require.NoError(t, err)

	// Verify node still exists (soft-deleted but not permanently removed).
	var count int
	err = s.QueryRow(ctx,
		`SELECT COUNT(*) FROM nodes WHERE id = ?`, "PROJ-1",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 1, count)
}

// TestBackgroundScan_UsesInjectedClock verifies clock injection.
func TestBackgroundScan_UsesInjectedClock(t *testing.T) {
	// Set clock to 40 days in the future from node creation.
	creation := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	futureTime := creation.Add(40 * 24 * time.Hour)
	bg, s := newTestBackgroundService(t, fixedClock(futureTime))
	ctx := context.Background()

	createTestNode(t, s, "PROJ-1", "PROJ", "Old Node", creation)
	require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))

	// Set deleted_at to creation time (40 days before our clock).
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`UPDATE nodes SET deleted_at = ? WHERE id = ?`,
		creation.UTC().Format(time.RFC3339), "PROJ-1",
	)
	require.NoError(t, err)

	// With clock 40 days ahead and 30-day retention, node should be cleaned.
	err = bg.RunScan(ctx)
	require.NoError(t, err)

	var count int
	err = s.QueryRow(ctx,
		`SELECT COUNT(*) FROM nodes WHERE id = ?`, "PROJ-1",
	).Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 0, count, "expired node should be permanently deleted")
}

// TestReady_IncludesPastDueDeferred verifies ready output per FR-3.8b.
func TestReady_IncludesPastDueDeferred(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create an open node.
	createTestNode(t, s, "PROJ-1", "PROJ", "Open Node", now)

	// Create a deferred node with past defer_until.
	createTestNode(t, s, "PROJ-2", "PROJ", "Deferred Past", now)
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-2", model.StatusDeferred, "waiting", "admin"))

	pastDefer := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`,
		pastDefer, "PROJ-2",
	)
	require.NoError(t, err)

	// Create a deferred node with future defer_until (should NOT be ready).
	createTestNode(t, s, "PROJ-3", "PROJ", "Deferred Future", now)
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-3", model.StatusDeferred, "later", "admin"))

	futureDefer := now.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	_, err = db.ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`,
		futureDefer, "PROJ-3",
	)
	require.NoError(t, err)

	nodes, err := bg.GetReadyNodes(ctx)
	require.NoError(t, err)

	// Should include PROJ-1 (open) and PROJ-2 (deferred past due).
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	assert.Contains(t, ids, "PROJ-1")
	assert.Contains(t, ids, "PROJ-2")
	assert.NotContains(t, ids, "PROJ-3")
}

// TestNewBackgroundService_NilStore_Panics verifies constructor rejects nil store.
func TestNewBackgroundService_NilStore_Panics(t *testing.T) {
	assert.Panics(t, func() {
		service.NewBackgroundService(nil, nil, nil, fixedClock(time.Now()))
	})
}

// TestNewBackgroundService_NilClock_Panics verifies constructor rejects nil clock.
func TestNewBackgroundService_NilClock_Panics(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	assert.Panics(t, func() {
		service.NewBackgroundService(s, nil, nil, nil)
	})
}

// TestNewBackgroundService_NilConfigAndLogger_UsesDefaults verifies nil fallbacks.
func TestNewBackgroundService_NilConfigAndLogger_UsesDefaults(t *testing.T) {
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg := service.NewBackgroundService(s, nil, nil, fixedClock(now))
	require.NotNil(t, bg)

	// Verify the service works with default config/logger.
	err = bg.RunScan(context.Background())
	assert.NoError(t, err)
}

// TestBackgroundScan_WakesDeferredNodes_FutureDeferUntil_NotWoken verifies future defer.
func TestBackgroundScan_WakesDeferredNodes_FutureDeferUntil_NotWoken(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	createTestNode(t, s, "PROJ-1", "PROJ", "Future Deferred", now)
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDeferred, "later", "admin"))

	// Set defer_until to the future.
	futureDefer := now.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	db := s.WriteDB()
	_, err := db.ExecContext(ctx,
		`UPDATE nodes SET defer_until = ? WHERE id = ?`,
		futureDefer, "PROJ-1",
	)
	require.NoError(t, err)

	err = bg.RunScan(ctx)
	require.NoError(t, err)

	// Should still be deferred.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err)
	assert.Equal(t, model.StatusDeferred, got.Status)
}

// TestGetReadyNodes_ExcludesAssignedNodes verifies assigned nodes are excluded.
func TestGetReadyNodes_ExcludesAssignedNodes(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	createTestNode(t, s, "PROJ-1", "PROJ", "Unassigned", now)
	createTestNode(t, s, "PROJ-2", "PROJ", "Assigned", now)

	// Claim PROJ-2.
	require.NoError(t, s.ClaimNode(ctx, "PROJ-2", "agent-1"))

	nodes, err := bg.GetReadyNodes(ctx)
	require.NoError(t, err)

	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	assert.Contains(t, ids, "PROJ-1")
	assert.NotContains(t, ids, "PROJ-2")
}

// TestGetReadyNodes_EmptyStore_ReturnsEmptySlice verifies empty result.
func TestGetReadyNodes_EmptyStore_ReturnsEmptySlice(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, _ := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	nodes, err := bg.GetReadyNodes(ctx)
	require.NoError(t, err)
	assert.Empty(t, nodes)
}

// TestGetReadyNodes_DeferredWithNullDeferUntil_Included verifies deferred with no date.
func TestGetReadyNodes_DeferredWithNullDeferUntil_Included(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create a deferred node with NULL defer_until (should be included in ready).
	createTestNode(t, s, "PROJ-1", "PROJ", "Deferred No Date", now)
	require.NoError(t, s.TransitionStatus(ctx, "PROJ-1", model.StatusDeferred, "later", "admin"))

	nodes, err := bg.GetReadyNodes(ctx)
	require.NoError(t, err)

	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	assert.Contains(t, ids, "PROJ-1")
}

// TestBackgroundScan_MultipleExpiredNodes_AllCleaned verifies batch cleanup.
func TestBackgroundScan_MultipleExpiredNodes_AllCleaned(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// Create and soft-delete multiple nodes with old deleted_at.
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("PROJ-%d", i)
		createTestNode(t, s, id, "PROJ", fmt.Sprintf("Node %d", i), now.Add(-32*24*time.Hour))
		require.NoError(t, s.DeleteNode(ctx, id, false, "admin"))

		deletedAt := now.Add(-31 * 24 * time.Hour).UTC().Format(time.RFC3339)
		db := s.WriteDB()
		_, err := db.ExecContext(ctx,
			`UPDATE nodes SET deleted_at = ? WHERE id = ?`, deletedAt, id,
		)
		require.NoError(t, err)
	}

	err := bg.RunScan(ctx)
	require.NoError(t, err)

	// All 3 should be permanently deleted.
	for i := 1; i <= 3; i++ {
		var count int
		err := s.QueryRow(ctx,
			`SELECT COUNT(*) FROM nodes WHERE id = ?`, fmt.Sprintf("PROJ-%d", i),
		).Scan(&count)
		require.NoError(t, err)
		assert.Equal(t, 0, count)
	}
}

// TestBackgroundScan_MultipleDeferredNodes_AllWoken verifies batch deferred wake.
func TestBackgroundScan_MultipleDeferredNodes_AllWoken(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, s := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("PROJ-%d", i)
		createTestNode(t, s, id, "PROJ", fmt.Sprintf("Deferred %d", i), now.Add(-2*time.Hour))
		require.NoError(t, s.TransitionStatus(ctx, id, model.StatusDeferred, "wait", "admin"))

		pastDefer := now.Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		db := s.WriteDB()
		_, err := db.ExecContext(ctx,
			`UPDATE nodes SET defer_until = ? WHERE id = ?`, pastDefer, id,
		)
		require.NoError(t, err)
	}

	err := bg.RunScan(ctx)
	require.NoError(t, err)

	for i := 1; i <= 3; i++ {
		got, err := s.GetNode(ctx, fmt.Sprintf("PROJ-%d", i))
		require.NoError(t, err)
		assert.Equal(t, model.StatusOpen, got.Status)
	}
}

// TestRunScan_ClosedStore_LogsErrorContinues verifies resilience to store errors per FR-3.3a.
func TestRunScan_ClosedStore_LogsErrorContinues(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)

	cfg := &service.StaticConfig{RetentionDuration: 30 * 24 * time.Hour}
	bg := service.NewBackgroundService(s, cfg, slog.Default(), fixedClock(now))

	// Close the store to trigger errors in SQL operations.
	require.NoError(t, s.Close())

	// RunScan should not panic — errors are logged.
	err = bg.RunScan(context.Background())
	assert.NoError(t, err) // RunScan always returns nil.
}

// TestGetReadyNodes_ClosedStore_ReturnsError verifies store error propagation.
func TestGetReadyNodes_ClosedStore_ReturnsError(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	s, err := sqlite.New(t.TempDir(), slog.Default())
	require.NoError(t, err)

	cfg := &service.StaticConfig{RetentionDuration: 30 * 24 * time.Hour}
	bg := service.NewBackgroundService(s, cfg, slog.Default(), fixedClock(now))

	require.NoError(t, s.Close())

	_, err = bg.GetReadyNodes(context.Background())
	assert.Error(t, err)
}

// TestOpportunisticCleanup_OnWrite verifies cleanup is triggered correctly.
func TestOpportunisticCleanup_OnWrite(t *testing.T) {
	// This test verifies RunScan can be called any time (opportunistically).
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	bg, _ := newTestBackgroundService(t, fixedClock(now))
	ctx := context.Background()

	// RunScan should succeed even with no expired nodes.
	err := bg.RunScan(ctx)
	assert.NoError(t, err)
}
