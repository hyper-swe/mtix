// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package format

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func makeTestNode() *model.Node {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	return &model.Node{
		ID:          "PROJ-1",
		ParentID:    "",
		Project:     "PROJ",
		Depth:       0,
		Seq:         1,
		Title:       "Test Node",
		Description: "A description",
		Prompt:      "Build the thing",
		Acceptance:  "It works",
		NodeType:    model.NodeTypeEpic,
		Priority:    model.PriorityCritical,
		Status:      model.StatusOpen,
		Progress:    0.5,
		Assignee:    "agent-a",
		Creator:     "cli",
		Weight:      1.0,
		ContentHash: "abc123",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
}

// TestProjectNode_ValidFields verifies that ProjectNode returns a map
// containing only the requested fields per FR-17.3.
func TestProjectNode_ValidFields(t *testing.T) {
	n := makeTestNode()
	result, err := ProjectNode(n, []string{"id", "title", "status"})
	require.NoError(t, err)
	assert.Len(t, result, 3)
	assert.Equal(t, "PROJ-1", result["id"])
	assert.Equal(t, "Test Node", result["title"])
	assert.Equal(t, model.StatusOpen, result["status"])
	// Ensure excluded fields are absent.
	_, hasPrompt := result["prompt"]
	assert.False(t, hasPrompt, "prompt must not be present when not requested")
}

// TestProjectNode_AllFields verifies that requesting all fields returns
// the complete node representation (no data loss).
func TestProjectNode_AllFields(t *testing.T) {
	n := makeTestNode()
	all := ValidFieldNames()
	result, err := ProjectNode(n, all)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result), 10, "all fields should be populated")
	assert.Equal(t, "PROJ-1", result["id"])
	assert.Equal(t, "Build the thing", result["prompt"])
}

// TestProjectNode_UnknownField verifies that an invalid field name
// returns ErrInvalidInput per FR-17.3.
func TestProjectNode_UnknownField(t *testing.T) {
	n := makeTestNode()
	_, err := ProjectNode(n, []string{"id", "nonexistent_field"})
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "nonexistent_field")
}

// TestProjectNode_EmptyFieldList verifies that an empty field list
// returns all fields (same as no projection).
func TestProjectNode_EmptyFieldList(t *testing.T) {
	n := makeTestNode()
	result, err := ProjectNode(n, nil)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result), 10)
}

// TestProjectNode_EmptySliceFieldList verifies that an explicitly empty
// slice also returns all fields.
func TestProjectNode_EmptySliceFieldList(t *testing.T) {
	n := makeTestNode()
	result, err := ProjectNode(n, []string{})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, len(result), 10)
}

// TestProjectNode_SingleField verifies projection to exactly one field.
func TestProjectNode_SingleField(t *testing.T) {
	n := makeTestNode()
	result, err := ProjectNode(n, []string{"prompt"})
	require.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "Build the thing", result["prompt"])
}

// TestProjectNode_FieldsAreCaseSensitive verifies that field names must
// match the JSON tag exactly (lowercase).
func TestProjectNode_FieldsAreCaseSensitive(t *testing.T) {
	n := makeTestNode()
	_, err := ProjectNode(n, []string{"ID"})
	require.Error(t, err, "uppercase ID is not a valid field name")
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestProjectNode_DuplicateFields verifies that duplicate fields don't
// cause issues — the result should contain the field once.
func TestProjectNode_DuplicateFields(t *testing.T) {
	n := makeTestNode()
	result, err := ProjectNode(n, []string{"id", "id", "title"})
	require.NoError(t, err)
	assert.Len(t, result, 2) // id + title (deduplicated)
}

// TestValidFieldNames verifies that the whitelist is non-empty and
// contains expected fields.
func TestValidFieldNames(t *testing.T) {
	fields := ValidFieldNames()
	assert.Greater(t, len(fields), 10, "should have many valid fields")
	// Spot-check expected fields.
	fieldSet := make(map[string]bool, len(fields))
	for _, f := range fields {
		fieldSet[f] = true
	}
	for _, expected := range []string{
		"id", "title", "description", "prompt", "acceptance",
		"status", "priority", "node_type", "assignee", "created_at",
	} {
		assert.True(t, fieldSet[expected], "field %q should be in valid set", expected)
	}
}

// TestProjectNodes_MultipleProjsects verifies the batch helper.
func TestProjectNodes_Multiple(t *testing.T) {
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	nodes := []*model.Node{
		{ID: "P-1", Title: "First", Status: model.StatusOpen, NodeType: model.NodeTypeEpic,
			Priority: 3, Weight: 1, CreatedAt: now, UpdatedAt: now, ContentHash: "h"},
		{ID: "P-2", Title: "Second", Status: model.StatusDone, NodeType: model.NodeTypeStory,
			Priority: 1, Weight: 1, CreatedAt: now, UpdatedAt: now, ContentHash: "h"},
	}

	results, err := ProjectNodes(nodes, []string{"id", "status"})
	require.NoError(t, err)
	require.Len(t, results, 2)
	assert.Equal(t, "P-1", results[0]["id"])
	assert.Equal(t, model.StatusDone, results[1]["status"])
}

// TestProjectNodes_InvalidField verifies batch projection rejects
// unknown fields on the first node.
func TestProjectNodes_InvalidField(t *testing.T) {
	nodes := []*model.Node{makeTestNode()}
	_, err := ProjectNodes(nodes, []string{"id", "bad_field"})
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestProjectNodes_EmptySliceWithFields verifies no panic on empty
// node slice with field projection per NASA-STD-8739.8 §4.5.2.
func TestProjectNodes_EmptySliceWithFields(t *testing.T) {
	results, err := ProjectNodes([]*model.Node{}, []string{"id", "title"})
	require.NoError(t, err)
	assert.Empty(t, results)
}

// TestProjectNodes_NilSliceWithFields verifies no panic on nil
// node slice with field projection.
func TestProjectNodes_NilSliceWithFields(t *testing.T) {
	results, err := ProjectNodes(nil, []string{"id", "title"})
	require.NoError(t, err)
	assert.Empty(t, results)
}
