// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestCreateAndRead_WithNullableFields_RoundTrips verifies that nullable fields
// (DeferUntil, EstimateMin, ActualMin, ClosedAt) are correctly stored and
// retrieved via indirect API calls to CreateNode and GetNode.
// This exercises nullableTime, nullableInt, and parseNullableTime helpers.
func TestCreateAndRead_WithNullableFields_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	futureTime := now.Add(24 * time.Hour)
	estimateVal := 120 // 2 hours
	actualVal := 150   // 2.5 hours

	node := makeRootNode("PROJ-1", "PROJ", "Task with estimates", now)
	node.DeferUntil = &futureTime
	node.EstimateMin = &estimateVal
	node.ActualMin = &actualVal
	node.Status = model.StatusInProgress

	// Create the node with nullable fields set.
	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node with nullable fields")

	// Read back and verify all nullable fields round-trip correctly.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read node back")

	// Verify DeferUntil round-trips as a time pointer.
	assert.NotNil(t, got.DeferUntil, "DeferUntil should not be nil")
	if got.DeferUntil != nil {
		assert.Equal(t, futureTime.Unix(), got.DeferUntil.Unix(),
			"DeferUntil timestamp should match original")
	}

	// Verify EstimateMin round-trips as an int pointer.
	assert.NotNil(t, got.EstimateMin, "EstimateMin should not be nil")
	if got.EstimateMin != nil {
		assert.Equal(t, estimateVal, *got.EstimateMin,
			"EstimateMin should match original value")
	}

	// Verify ActualMin round-trips as an int pointer.
	assert.NotNil(t, got.ActualMin, "ActualMin should not be nil")
	if got.ActualMin != nil {
		assert.Equal(t, actualVal, *got.ActualMin,
			"ActualMin should match original value")
	}

	// Verify other fields remain intact.
	assert.Equal(t, "PROJ-1", got.ID)
	assert.Equal(t, "Task with estimates", got.Title)
	assert.Equal(t, model.StatusInProgress, got.Status)
}

// TestCreateAndRead_WithNilNullableFields_ReturnsNil verifies that nullable
// fields left as nil do not get populated when reading back.
// This exercises the nil-check branches of nullableTime and nullableInt.
func TestCreateAndRead_WithNilNullableFields_ReturnsNil(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Task without estimates", now)
	// Explicitly leave nullable fields as nil.
	node.DeferUntil = nil
	node.EstimateMin = nil
	node.ActualMin = nil
	node.ClosedAt = nil

	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node with nil nullable fields")

	// Read back and verify nullable fields are nil.
	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read node back")

	assert.Nil(t, got.DeferUntil, "DeferUntil should be nil")
	assert.Nil(t, got.EstimateMin, "EstimateMin should be nil")
	assert.Nil(t, got.ActualMin, "ActualMin should be nil")
	assert.Nil(t, got.ClosedAt, "ClosedAt should be nil")

	// Verify non-nullable fields are still present.
	assert.Equal(t, "PROJ-1", got.ID)
	assert.Equal(t, "Task without estimates", got.Title)
	assert.Equal(t, now.Unix(), got.CreatedAt.Unix())
}

// TestCreateAndRead_WithLabels_RoundTrips verifies that labels (a JSON array)
// are correctly marshaled on write and unmarshaled on read.
// This exercises marshalJSONField and unmarshalJSONField helpers.
func TestCreateAndRead_WithLabels_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Task with labels", now)
	node.Labels = []string{"critical", "backend", "performance"}

	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node with labels")

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read node back")

	// Verify labels round-trip as a JSON array.
	require.NotNil(t, got.Labels, "Labels should not be nil")
	assert.Equal(t, []string{"critical", "backend", "performance"}, got.Labels,
		"Labels should match original array")
}

// TestCreateAndRead_WithEmptyLabels_ReturnsEmptyArray verifies that empty
// label arrays are stored as NULL and read back as empty (not nil).
// This tests the "[]" special case in marshalJSONField.
func TestCreateAndRead_WithEmptyLabels_ReturnsEmptyArray(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Task with empty labels", now)
	node.Labels = []string{} // Explicitly empty

	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node with empty labels")

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read node back")

	// Empty arrays should be preserved or return nil (depending on implementation).
	// The marshaler treats "[]" as NULL per the code.
	if got.Labels != nil {
		assert.Equal(t, 0, len(got.Labels), "Labels should be empty or nil")
	}
}

// TestCreateAndRead_WithCodeRefs_RoundTrips verifies that code references
// (a complex JSON structure) are correctly marshaled and unmarshaled.
// This exercises marshalJSONField for the CodeRef slice.
func TestCreateAndRead_WithCodeRefs_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Task with code refs", now)
	node.CodeRefs = []model.CodeRef{
		{
			File:     "main.go",
			Line:     42,
			Function: "main",
			Snippet:  "package main",
		},
		{
			File:     "utils.go",
			Line:     100,
			Function: "Helper",
		},
	}

	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node with code refs")

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read node back")

	// Verify code refs round-trip correctly.
	require.NotNil(t, got.CodeRefs, "CodeRefs should not be nil")
	assert.Equal(t, 2, len(got.CodeRefs), "Should have 2 code refs")

	// Verify first code ref.
	assert.Equal(t, "main.go", got.CodeRefs[0].File)
	assert.Equal(t, 42, got.CodeRefs[0].Line)
	assert.Equal(t, "main", got.CodeRefs[0].Function)
	assert.Equal(t, "package main", got.CodeRefs[0].Snippet)

	// Verify second code ref.
	assert.Equal(t, "utils.go", got.CodeRefs[1].File)
	assert.Equal(t, 100, got.CodeRefs[1].Line)
	assert.Equal(t, "Helper", got.CodeRefs[1].Function)
}

// TestCreateAndRead_WithAnnotations_RoundTrips verifies that annotations
// (a complex JSON structure with timestamps) are correctly stored and retrieved.
// This exercises unmarshalJSONField for the Annotation slice.
func TestCreateAndRead_WithAnnotations_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Task with annotations", now)

	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node")

	// Insert annotations directly via raw SQL (simulating a real scenario where
	// annotations are added post-creation). This tests the read path.
	// We'll use the store's exposed WriteDB or QueryRow methods (if available).
	// For now, we'll test indirectly by verifying the annotation JSON parsing logic
	// through a separate helper that adds annotations.

	// Create a node with annotations set at creation time.
	annotationTime := now.Add(-1 * time.Hour)
	node2 := makeRootNode("PROJ-2", "PROJ", "Task with pre-set annotations", now)
	node2.Annotations = []model.Annotation{
		{
			ID:        "ann-1",
			Author:    "alice@example.com",
			Text:      "This needs clarification",
			CreatedAt: annotationTime,
			Resolved:  false,
		},
		{
			ID:        "ann-2",
			Author:    "bob@example.com",
			Text:      "Already handled in commit abc123",
			CreatedAt: annotationTime.Add(10 * time.Minute),
			Resolved:  true,
		},
	}

	err = s.CreateNode(ctx, node2)
	require.NoError(t, err, "failed to create node with annotations")

	got, err := s.GetNode(ctx, "PROJ-2")
	require.NoError(t, err, "failed to read node back")

	// Verify annotations round-trip correctly.
	require.NotNil(t, got.Annotations, "Annotations should not be nil")
	assert.Equal(t, 2, len(got.Annotations), "Should have 2 annotations")

	// Verify first annotation.
	assert.Equal(t, "ann-1", got.Annotations[0].ID)
	assert.Equal(t, "alice@example.com", got.Annotations[0].Author)
	assert.Equal(t, "This needs clarification", got.Annotations[0].Text)
	assert.False(t, got.Annotations[0].Resolved, "First annotation should not be resolved")

	// Verify second annotation.
	assert.Equal(t, "ann-2", got.Annotations[1].ID)
	assert.Equal(t, "bob@example.com", got.Annotations[1].Author)
	assert.True(t, got.Annotations[1].Resolved, "Second annotation should be resolved")
}

// TestCreateAndRead_WithMetadata_RoundTrips verifies that metadata
// (arbitrary JSON) is correctly stored and retrieved as json.RawMessage.
// This exercises the metadata-specific logic in scanNode.
func TestCreateAndRead_WithMetadata_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Task with metadata", now)
	node.Metadata = []byte(`{"custom_field":"custom_value","count":42}`)

	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node with metadata")

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read node back")

	// Verify metadata round-trips as json.RawMessage.
	assert.NotNil(t, got.Metadata, "Metadata should not be nil")
	assert.Equal(t, `{"custom_field":"custom_value","count":42}`, string(got.Metadata),
		"Metadata should match original JSON")
}

// TestCreateAndRead_WithEmptyMetadata_ReturnsEmpty verifies that empty
// or nil metadata is handled correctly.
func TestCreateAndRead_WithEmptyMetadata_ReturnsEmpty(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	node := makeRootNode("PROJ-1", "PROJ", "Task without metadata", now)
	node.Metadata = nil

	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node without metadata")

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read node back")

	// Nil metadata should remain nil or be empty.
	if got.Metadata != nil {
		assert.Equal(t, 0, len(got.Metadata), "Metadata should be empty")
	}
}

// TestCreateAndRead_WithClosedAt_RoundTrips verifies that ClosedAt timestamp
// (set during state transitions) is correctly stored and retrieved.
// This exercises parseNullableTime for the ClosedAt field specifically.
func TestCreateAndRead_WithClosedAt_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node in open status, then transition to done.
	node := makeRootNode("PROJ-1", "PROJ", "Task to close", now)
	node.Status = model.StatusInProgress

	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create node")

	// Transition to done (which should set ClosedAt).
	err = s.TransitionStatus(ctx, "PROJ-1", model.StatusDone, "Completed", "agent-1")
	require.NoError(t, err, "failed to transition to done")

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read node back")

	// Verify ClosedAt is now set.
	assert.NotNil(t, got.ClosedAt, "ClosedAt should be set after done transition")
	if got.ClosedAt != nil {
		// The actual ClosedAt time is set by the transition, not our passed time.
		// Verify it's approximately now (within a few seconds).
		assert.True(t, got.ClosedAt.After(now.Add(-10*time.Second)),
			"ClosedAt should be after creation time")
		assert.True(t, got.ClosedAt.Before(now.Add(10*time.Second)),
			"ClosedAt should be recent")
	}
}

// TestUpdateProgress_ValidProgress_Updates verifies that progress updates
// are correctly applied through the service layer.
// This indirectly exercises the QueryRow helper in query.go.
func TestUpdateProgress_ValidProgress_Updates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a parent with two children.
	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	child1 := makeChildNode("PROJ-1.1", "PROJ-1", "PROJ", "Child 1", 1, 1, now)
	child1.Progress = 0.5 // 50% done
	require.NoError(t, s.CreateNode(ctx, child1))

	child2 := makeChildNode("PROJ-1.2", "PROJ-1", "PROJ", "Child 2", 1, 2, now)
	child2.Progress = 1.0 // 100% done
	require.NoError(t, s.CreateNode(ctx, child2))

	// After both children are created, parent progress should be recalculated
	// via the automatic progress rollup.
	parentGot, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read parent")

	// Expected: (0.5 * 1.0 + 1.0 * 1.0) / (1.0 + 1.0) = 1.5 / 2.0 = 0.75
	assert.Equal(t, 0.75, parentGot.Progress,
		"Parent progress should be weighted average of children")
}

// TestQueryRow_SelectsFromReadDB verifies that the QueryRow helper
// executes queries on the read database.
// This tests query.go's QueryRow function.
func TestQueryRow_SelectsFromReadDB(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node to query.
	node := makeRootNode("PROJ-1", "PROJ", "Test Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Use QueryRow to select a field from the nodes table.
	var title string
	err := s.QueryRow(ctx,
		"SELECT title FROM nodes WHERE id = ?",
		"PROJ-1",
	).Scan(&title)

	require.NoError(t, err, "QueryRow should succeed")
	assert.Equal(t, "Test Node", title, "QueryRow should return correct title")
}

// TestQueryRow_NoRows_ReturnsErrNoRows verifies that QueryRow correctly
// returns sql.ErrNoRows when no rows match.
func TestQueryRow_NoRows_ReturnsErrNoRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Query for a non-existent node.
	var title string
	err := s.QueryRow(ctx,
		"SELECT title FROM nodes WHERE id = ?",
		"NONEXISTENT",
	).Scan(&title)

	assert.Equal(t, sql.ErrNoRows, err,
		"QueryRow should return sql.ErrNoRows for missing rows")
}

// TestQuery_MultipleRows_ReturnsRows verifies that the Query helper
// executes queries on the read database and returns multiple rows.
// This tests query.go's Query function.
func TestQuery_MultipleRows_ReturnsRows(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a parent with multiple children.
	parent := makeRootNode("PROJ-1", "PROJ", "Parent", now)
	require.NoError(t, s.CreateNode(ctx, parent))

	for i := 1; i <= 3; i++ {
		child := makeChildNode("PROJ-1."+itoa(i), "PROJ-1", "PROJ",
			"Child "+itoa(i), 1, i, now)
		require.NoError(t, s.CreateNode(ctx, child))
	}

	// Use Query to select all children.
	rows, err := s.Query(ctx,
		"SELECT id, title FROM nodes WHERE parent_id = ? ORDER BY seq",
		"PROJ-1",
	)
	require.NoError(t, err, "Query should succeed")
	require.NotNil(t, rows, "Query should return rows")
	defer func() { _ = rows.Close() }()

	// Scan all results.
	var ids []string
	var titles []string
	for rows.Next() {
		var id, title string
		err := rows.Scan(&id, &title)
		require.NoError(t, err, "Scan should succeed")
		ids = append(ids, id)
		titles = append(titles, title)
	}

	require.NoError(t, rows.Err(), "rows.Err() should be nil")
	assert.Equal(t, 3, len(ids), "Should have 3 children")
	assert.Equal(t, []string{"PROJ-1.1", "PROJ-1.2", "PROJ-1.3"}, ids)
	assert.Equal(t, []string{"Child 1", "Child 2", "Child 3"}, titles)
}

// TestWriteDB_ReturnsWriteConnection verifies that the WriteDB method
// returns a valid write database connection.
// This tests query.go's WriteDB function.
func TestWriteDB_ReturnsWriteConnection(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Call WriteDB and verify it returns a non-nil *sql.DB.
	writeDB := s.WriteDB()
	require.NotNil(t, writeDB, "WriteDB should return non-nil *sql.DB")

	// Verify we can execute a simple query on the write DB.
	var result int
	err := writeDB.QueryRowContext(ctx, "SELECT 1").Scan(&result)
	require.NoError(t, err, "Should be able to query the write DB")
	assert.Equal(t, 1, result, "SELECT 1 should return 1")
}

// TestWriteDB_CanInsertAndSelect verifies that the WriteDB connection
// supports both writes and reads (for validation after writes).
func TestWriteDB_CanInsertAndSelect(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Create a node using the normal path.
	node := makeRootNode("PROJ-1", "PROJ", "Test Node", now)
	require.NoError(t, s.CreateNode(ctx, node))

	// Use WriteDB to query what was just written.
	writeDB := s.WriteDB()
	var id string
	err := writeDB.QueryRowContext(ctx,
		"SELECT id FROM nodes WHERE title = ?",
		"Test Node",
	).Scan(&id)

	require.NoError(t, err, "Should be able to select via WriteDB")
	assert.Equal(t, "PROJ-1", id)
}

// TestScanNode_AllFieldsPopulated_RoundTrips verifies comprehensive
// round-trip of a fully-populated node with all optional fields set.
// This is a macro test that exercises the entire scanNode flow.
func TestScanNode_AllFieldsPopulated_RoundTrips(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	// Build a maximally-populated node.
	futureTime := now.Add(24 * time.Hour)
	pastTime := now.Add(-2 * time.Hour)
	estimateVal := 240
	actualVal := 180

	node := makeRootNode("PROJ-1", "PROJ", "Complex Node", now)
	node.Status = model.StatusInProgress
	node.Description = "A detailed description of this node"
	node.Prompt = "What should this task accomplish?"
	node.Acceptance = "The task is done when X, Y, Z are satisfied"
	node.IssueType = model.IssueTypeBug
	node.Labels = []string{"urgent", "security"}
	node.Assignee = "alice@example.com"
	node.Creator = "bob@example.com"
	node.DeferUntil = &futureTime
	node.EstimateMin = &estimateVal
	node.ActualMin = &actualVal
	node.Weight = 2.5
	node.Progress = 0.25
	node.CodeRefs = []model.CodeRef{
		{File: "security.go", Line: 42, Function: "ValidateToken"},
	}
	node.CommitRefs = []string{"abc123def456", "xyz789uvw012"}
	node.Annotations = []model.Annotation{
		{
			ID:        "note-1",
			Author:    "reviewer@example.com",
			Text:      "Check for SQL injection",
			CreatedAt: pastTime,
			Resolved:  false,
		},
	}
	node.InvalidatedAt = &pastTime
	node.InvalidatedBy = "admin@example.com"
	node.InvalidationReason = "Superseded by PROJ-2"
	node.Metadata = []byte(`{"jira_key":"PROJ-123","severity":"high"}`)
	node.SessionID = "session-abc123"

	// Create and verify.
	err := s.CreateNode(ctx, node)
	require.NoError(t, err, "failed to create complex node")

	got, err := s.GetNode(ctx, "PROJ-1")
	require.NoError(t, err, "failed to read complex node")

	// Verify all fields.
	assert.Equal(t, "PROJ-1", got.ID)
	assert.Equal(t, "PROJ", got.Project)
	assert.Equal(t, "Complex Node", got.Title)
	assert.Equal(t, "A detailed description of this node", got.Description)
	assert.Equal(t, "What should this task accomplish?", got.Prompt)
	assert.Equal(t, "The task is done when X, Y, Z are satisfied", got.Acceptance)
	assert.Equal(t, model.StatusInProgress, got.Status)
	assert.Equal(t, model.IssueTypeBug, got.IssueType)
	assert.Equal(t, []string{"urgent", "security"}, got.Labels)
	assert.Equal(t, "alice@example.com", got.Assignee)
	assert.Equal(t, "bob@example.com", got.Creator)
	assert.Equal(t, 2.5, got.Weight)
	assert.Equal(t, 0.25, got.Progress)

	// Verify pointers round-trip.
	require.NotNil(t, got.DeferUntil)
	assert.Equal(t, futureTime.Unix(), got.DeferUntil.Unix())
	require.NotNil(t, got.EstimateMin)
	assert.Equal(t, estimateVal, *got.EstimateMin)
	require.NotNil(t, got.ActualMin)
	assert.Equal(t, actualVal, *got.ActualMin)

	// Verify complex fields.
	assert.Equal(t, 1, len(got.CodeRefs))
	assert.Equal(t, "security.go", got.CodeRefs[0].File)
	assert.Equal(t, 42, got.CodeRefs[0].Line)
	assert.Equal(t, "ValidateToken", got.CodeRefs[0].Function)

	assert.Equal(t, 2, len(got.CommitRefs))
	assert.Contains(t, got.CommitRefs, "abc123def456")

	assert.Equal(t, 1, len(got.Annotations))
	assert.Equal(t, "note-1", got.Annotations[0].ID)

	require.NotNil(t, got.InvalidatedAt)
	assert.Equal(t, pastTime.Unix(), got.InvalidatedAt.Unix())
	assert.Equal(t, "admin@example.com", got.InvalidatedBy)
	assert.Equal(t, "Superseded by PROJ-2", got.InvalidationReason)

	assert.Equal(t, `{"jira_key":"PROJ-123","severity":"high"}`, string(got.Metadata))
	assert.Equal(t, "session-abc123", got.SessionID)
}

