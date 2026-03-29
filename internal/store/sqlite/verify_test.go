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

// TestVerify_CleanDB_AllChecksPassing verifies all checks pass on a clean DB.
func TestVerify_CleanDB_AllChecksPassing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a simple tree with consistent data.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "V-1", Project: "V", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Ensure sequence is consistent.
	_, err := s.NextSequence(ctx, "V:")
	require.NoError(t, err)

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, result.IntegrityOK, "integrity check should pass")
	assert.True(t, result.ForeignKeyOK, "foreign key check should pass")
	assert.True(t, result.FTSOK, "FTS check should pass")
	assert.True(t, result.AllPassed, "all checks should pass")
	assert.Empty(t, result.Errors, "should have no errors")
}

// TestVerify_EmptyDB_AllChecksPassing verifies all checks pass on empty DB.
func TestVerify_EmptyDB_AllChecksPassing(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	result, err := s.Verify(ctx)
	require.NoError(t, err)

	assert.True(t, result.AllPassed, "empty DB should pass all checks")
}

// TestVerify_SequenceInconsistency_Detected verifies stale sequences are detected.
func TestVerify_SequenceInconsistency_Detected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node with seq=5 but don't update the sequences table to match.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "V-1", Project: "V", Depth: 0, Seq: 5, Title: "High seq node",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Set sequence counter to a value lower than the max seq.
	_, err := s.WriteDB().ExecContext(ctx,
		"INSERT OR REPLACE INTO sequences (key, value) VALUES (?, ?)", "V:", 2)
	require.NoError(t, err)

	result, err := s.Verify(ctx)
	require.NoError(t, err)

	assert.False(t, result.SequenceOK, "should detect sequence inconsistency")
	assert.False(t, result.AllPassed)
	assert.NotEmpty(t, result.Errors)

	// Verify error message contains useful info.
	found := false
	for _, e := range result.Errors {
		if assert.Contains(t, e, "sequence_check") {
			found = true
		}
	}
	assert.True(t, found, "should have a sequence_check error")
}

// TestVerify_ProgressInconsistency_Detected verifies mismatched progress detection.
func TestVerify_ProgressInconsistency_Detected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create parent and child normally (trigger will set correct progress).
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "V-1", Project: "V", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "V-1.1", ParentID: "V-1", Project: "V", Depth: 1, Seq: 1, Title: "Child",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h2", Progress: 0.0,
		CreatedAt: now, UpdatedAt: now,
	}))

	// Manually corrupt parent progress AFTER child creation (bypass recalculation trigger).
	_, err := s.WriteDB().ExecContext(ctx, "UPDATE nodes SET progress = 0.9 WHERE id = 'V-1'")
	require.NoError(t, err)

	result, err := s.Verify(ctx)
	require.NoError(t, err)

	assert.False(t, result.ProgressOK, "should detect progress inconsistency")
	assert.False(t, result.AllPassed)
}

// TestVerify_IntegrityAndFKChecksWork verifies PRAGMA checks work.
func TestVerify_IntegrityAndFKChecksWork(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	result, err := s.Verify(ctx)
	require.NoError(t, err)

	// On a clean store, both should pass.
	assert.True(t, result.IntegrityOK)
	assert.True(t, result.ForeignKeyOK)
}

// TestVerify_MissingSequence_Detected verifies missing sequence entry detection.
func TestVerify_MissingSequence_Detected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node, which creates a sequence entry.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "V-1", Project: "V", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Delete the sequence entry to create inconsistency.
	_, err := s.WriteDB().ExecContext(ctx, "DELETE FROM sequences")
	require.NoError(t, err)

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.False(t, result.SequenceOK, "should detect missing sequence")
	assert.False(t, result.AllPassed)
}

// TestVerify_FTSConsistent_AfterOperations verifies FTS consistency post-CRUD.
func TestVerify_FTSConsistent_AfterOperations(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create, then delete — FTS should still be consistent.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "V-1", Project: "V", Depth: 0, Seq: 1, Title: "FTS Test",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))
	require.NoError(t, s.DeleteNode(ctx, "V-1", false, "admin"))

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.FTSOK, "FTS should be consistent after delete")
}

// TestVerify_ForeignKeyViolation_Detected verifies FK check detects violations.
func TestVerify_ForeignKeyViolation_Detected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node, then directly corrupt the parent_id to a nonexistent node.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "V-1", Project: "V", Depth: 0, Seq: 1, Title: "Root",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeIssue, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))

	// Temporarily disable FK checks to create violation.
	_, err := s.WriteDB().ExecContext(ctx, "PRAGMA foreign_keys = OFF")
	require.NoError(t, err)

	_, err = s.WriteDB().ExecContext(ctx,
		`INSERT INTO nodes (id, parent_id, depth, seq, project, title,
		 status, priority, weight, node_type, content_hash, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		"V-BAD", "NONEXISTENT", 1, 1, "V", "Bad node",
		"open", 3, 1.0, "issue", "hbad", now.Format(time.RFC3339), now.Format(time.RFC3339),
	)
	require.NoError(t, err)

	// Re-enable FK checks.
	_, err = s.WriteDB().ExecContext(ctx, "PRAGMA foreign_keys = ON")
	require.NoError(t, err)

	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.False(t, result.ForeignKeyOK, "should detect FK violation")
	assert.False(t, result.AllPassed)
}

// TestVerify_ProgressConsistent_WithCancelledChildren verifies FR-5.4 progress.
func TestVerify_ProgressConsistent_WithCancelledChildren(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create parent with two children: one done, one cancelled.
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "V-1", Project: "V", Depth: 0, Seq: 1, Title: "Parent",
		Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
		NodeType: model.NodeTypeStory, ContentHash: "h1",
		CreatedAt: now, UpdatedAt: now,
	}))
	child1 := makeChildNode("V-1.1", "V-1", "V", "Done Child", 1, 1, now)
	child1.Status = model.StatusDone
	child1.Progress = 1.0
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("V-1.2", "V-1", "V", "To Cancel", 1, 2, now)
	require.NoError(t, s.CreateNode(ctx, child2))

	require.NoError(t, s.CancelNode(ctx, "V-1.2", "not needed", "pm", false))

	// Set up sequences to match node data so verifySequences passes.
	_, seqErr := s.WriteDB().ExecContext(ctx,
		"INSERT OR REPLACE INTO sequences (key, value) VALUES (?, ?)", "V:", 1)
	require.NoError(t, seqErr)
	_, seqErr = s.WriteDB().ExecContext(ctx,
		"INSERT OR REPLACE INTO sequences (key, value) VALUES (?, ?)", "V:V-1", 2)
	require.NoError(t, seqErr)

	// After cancel, all checks should pass.
	result, err := s.Verify(ctx)
	require.NoError(t, err)
	assert.True(t, result.ProgressOK, "progress should be consistent after cancel")
	assert.True(t, result.AllPassed, "all checks should pass")
}
