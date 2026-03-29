// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestBackup_CreatesValidCopy verifies backup creates a readable copy per FR-6.3a.
func TestBackup_CreatesValidCopy(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create some data.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "BK-1", Project: "BK", Depth: 0, Seq: 1, Title: "Backup test node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	destPath := filepath.Join(t.TempDir(), "backup.db")
	result, err := s.Backup(ctx, destPath)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.Equal(t, destPath, result.Path)
	assert.True(t, result.Size > 0, "backup should have non-zero size")
	assert.True(t, result.Verified, "backup should be verified")

	// Verify the file exists.
	info, err := os.Stat(destPath)
	require.NoError(t, err)
	assert.Equal(t, result.Size, info.Size())
}

// TestBackup_VerificationPasses verifies PRAGMA quick_check passes on good backup.
func TestBackup_VerificationPasses(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	destPath := filepath.Join(t.TempDir(), "backup_verify.db")
	result, err := s.Backup(ctx, destPath)
	require.NoError(t, err)
	assert.True(t, result.Verified)
}

// TestBackup_EmptyDestPath_ReturnsError verifies empty path is rejected.
func TestBackup_EmptyDestPath_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	_, err := s.Backup(ctx, "")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "destination path is required")
}

// TestBackup_PreservesData verifies backup contains the same data as source.
func TestBackup_PreservesData(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create test data.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "BK-1", Project: "BK", Depth: 0, Seq: 1, Title: "Original data",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1", CreatedAt: now, UpdatedAt: now,
	}))

	destPath := filepath.Join(t.TempDir(), "backup_data.db")
	_, err := s.Backup(ctx, destPath)
	require.NoError(t, err)

	// Open backup and verify node exists.
	db := newTestDB(t, destPath)
	var title string
	err = db.QueryRow("SELECT title FROM nodes WHERE id = ?", "BK-1").Scan(&title)
	require.NoError(t, err)
	assert.Equal(t, "Original data", title)
}

// TestBackup_InvalidPath_ReturnsError verifies error on invalid destination path.
func TestBackup_InvalidPath_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Use a path that cannot be written to.
	_, err := s.Backup(ctx, "/nonexistent/deeply/nested/dir/backup.db")
	require.Error(t, err)
}

// TestBackup_MultipleNodes_PreservesAll verifies backup of multiple nodes per FR-6.3a.
func TestBackup_MultipleNodes_PreservesAll(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	for i := 1; i <= 5; i++ {
		require.NoError(t, s.CreateNode(ctx, &model.Node{
			ID: "BK-" + itoa(i), Project: "BK", Depth: 0, Seq: i,
			Title:       "Backup node " + itoa(i),
			Status:      model.StatusOpen,
			Priority:    model.PriorityMedium,
			Weight:      1.0,
			NodeType:    model.NodeTypeIssue,
			ContentHash: "h" + itoa(i),
			CreatedAt:   now.Add(time.Duration(i) * time.Second),
			UpdatedAt:   now.Add(time.Duration(i) * time.Second),
		}))
	}

	destPath := filepath.Join(t.TempDir(), "backup_multi.db")
	result, err := s.Backup(ctx, destPath)
	require.NoError(t, err)
	assert.True(t, result.Verified)

	// Verify all 5 nodes exist in backup.
	db := newTestDB(t, destPath)
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM nodes").Scan(&count)
	require.NoError(t, err)
	assert.Equal(t, 5, count)
}

// TestBackup_EmptyDB_Succeeds verifies backup of empty database per FR-6.3a.
func TestBackup_EmptyDB_Succeeds(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	destPath := filepath.Join(t.TempDir(), "backup_empty.db")
	result, err := s.Backup(ctx, destPath)
	require.NoError(t, err)
	assert.True(t, result.Verified)
	assert.True(t, result.Size > 0, "even empty DB backup should have non-zero size")
}
