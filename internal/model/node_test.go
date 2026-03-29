// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestNode_JSONSerialization_MatchesAPIContract verifies that JSON struct tags
// use snake_case and all fields serialize correctly.
func TestNode_JSONSerialization_MatchesAPIContract(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	closedAt := time.Date(2026, 3, 10, 14, 0, 0, 0, time.UTC)

	node := model.Node{
		ID:        "PROJ-1.2.3",
		ParentID:  "PROJ-1.2",
		Project:   "PROJ",
		Depth:     2,
		Seq:       3,
		Title:     "Test Issue",
		Status:    model.StatusOpen,
		Priority:  model.PriorityHigh,
		NodeType:  model.NodeTypeIssue,
		Weight:    1.5,
		CreatedAt: now,
		UpdatedAt: now,
		ClosedAt:  &closedAt,
		Labels:    []string{"backend", "api"},
	}

	data, err := json.Marshal(node)
	require.NoError(t, err)

	jsonStr := string(data)

	// Verify snake_case field names per CODING-STYLE.md.
	assert.Contains(t, jsonStr, `"id"`)
	assert.Contains(t, jsonStr, `"parent_id"`)
	assert.Contains(t, jsonStr, `"created_at"`)
	assert.Contains(t, jsonStr, `"updated_at"`)
	assert.Contains(t, jsonStr, `"closed_at"`)
	assert.Contains(t, jsonStr, `"node_type"`)

	// Verify no PascalCase field names leaked through.
	assert.NotContains(t, jsonStr, `"ParentID"`)
	assert.NotContains(t, jsonStr, `"CreatedAt"`)
	assert.NotContains(t, jsonStr, `"NodeType"`)

	// Verify round-trip.
	var decoded model.Node
	err = json.Unmarshal(data, &decoded)
	require.NoError(t, err)
	assert.Equal(t, node.ID, decoded.ID)
	assert.Equal(t, node.ParentID, decoded.ParentID)
	assert.Equal(t, node.Title, decoded.Title)
	assert.Equal(t, node.Priority, decoded.Priority)
}

// TestNode_Validate_ValidNode_NoError verifies a well-formed node passes validation.
func TestNode_Validate_ValidNode_NoError(t *testing.T) {
	node := &model.Node{
		Title:    "Valid node",
		Status:   model.StatusOpen,
		Priority: model.PriorityMedium,
	}

	err := node.Validate()
	assert.NoError(t, err)
}

// TestNode_Validate_EmptyTitle_ReturnsError verifies empty title is rejected.
func TestNode_Validate_EmptyTitle_ReturnsError(t *testing.T) {
	node := &model.Node{
		Title: "",
	}

	err := node.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "title")
}

// TestNode_Validate_TitleTooLong_ReturnsError verifies titles over 500 chars are rejected.
func TestNode_Validate_TitleTooLong_ReturnsError(t *testing.T) {
	node := &model.Node{
		Title: strings.Repeat("a", model.MaxTitleLength+1),
	}

	err := node.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "title")
	assert.Contains(t, err.Error(), "maximum")
}

// TestNode_Validate_InvalidStatus_ReturnsError verifies unrecognized statuses are rejected.
func TestNode_Validate_InvalidStatus_ReturnsError(t *testing.T) {
	node := &model.Node{
		Title:  "Valid title",
		Status: model.Status("nonexistent"),
	}

	err := node.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "status")
}

// TestNode_Validate_InvalidPriority_ReturnsError verifies out-of-range priorities are rejected.
func TestNode_Validate_InvalidPriority_ReturnsError(t *testing.T) {
	tests := []struct {
		name     string
		priority model.Priority
	}{
		{"zero", 0},
		{"negative", -1},
		{"too_high", 6},
		{"way_too_high", 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			node := &model.Node{
				Title:    "Valid title",
				Status:   model.StatusOpen,
				Priority: tt.priority,
			}

			err := node.Validate()
			// Priority 0 means "not set" and should be allowed.
			if tt.priority == 0 {
				assert.NoError(t, err, "priority 0 (unset) should be allowed")
				return
			}
			require.Error(t, err)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Contains(t, err.Error(), "priority")
		})
	}
}

// TestNode_Validate_DescriptionTooLarge_ReturnsError verifies oversized descriptions are rejected.
func TestNode_Validate_DescriptionTooLarge_ReturnsError(t *testing.T) {
	node := &model.Node{
		Title:       "Valid title",
		Description: strings.Repeat("x", model.MaxDescriptionSize+1),
	}

	err := node.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "description")
}

// TestNode_Validate_PromptTooLarge_ReturnsError verifies oversized prompts are rejected.
func TestNode_Validate_PromptTooLarge_ReturnsError(t *testing.T) {
	node := &model.Node{
		Title:  "Valid title",
		Prompt: strings.Repeat("x", model.MaxPromptSize+1),
	}

	err := node.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "prompt")
}
