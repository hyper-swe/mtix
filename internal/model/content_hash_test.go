// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestContentHash_SameContent_SameHash verifies deterministic hashing.
func TestContentHash_SameContent_SameHash(t *testing.T) {
	hash1 := model.ComputeContentHash(
		"Fix login bug",
		"Users can't log in with special chars",
		"Investigate and fix the auth flow",
		"Login works with special characters",
		[]string{"backend", "auth"},
	)

	hash2 := model.ComputeContentHash(
		"Fix login bug",
		"Users can't log in with special chars",
		"Investigate and fix the auth flow",
		"Login works with special characters",
		[]string{"backend", "auth"},
	)

	assert.Equal(t, hash1, hash2, "same content should produce same hash")
	assert.Len(t, hash1, 64, "SHA256 hex should be 64 characters")
}

// TestContentHash_DifferentContent_DifferentHash verifies different content produces different hashes.
func TestContentHash_DifferentContent_DifferentHash(t *testing.T) {
	tests := []struct {
		name   string
		title1 string
		title2 string
	}{
		{"different_title", "Title A", "Title B"},
		{"case_sensitive", "Title", "title"},
		{"whitespace", "Title", "Title "},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := model.ComputeContentHash(tt.title1, "", "", "", nil)
			hash2 := model.ComputeContentHash(tt.title2, "", "", "", nil)
			assert.NotEqual(t, hash1, hash2, "different content should produce different hash")
		})
	}
}

// TestContentHash_StatusChange_HashUnchanged verifies state fields don't affect hash.
func TestContentHash_StatusChange_HashUnchanged(t *testing.T) {
	// Content hash is computed from title/description/prompt/acceptance/labels only.
	// Status, priority, timestamps are NOT included per FR-3.7.
	node1 := &model.Node{
		Title:       "Fix bug",
		Description: "desc",
		Prompt:      "prompt",
		Acceptance:  "acceptance",
		Labels:      []string{"label"},
		Status:      model.StatusOpen,
		Priority:    model.PriorityCritical,
	}

	node2 := &model.Node{
		Title:       "Fix bug",
		Description: "desc",
		Prompt:      "prompt",
		Acceptance:  "acceptance",
		Labels:      []string{"label"},
		Status:      model.StatusDone,          // Different status
		Priority:    model.PriorityBacklog,     // Different priority
	}

	assert.Equal(t, node1.ComputeHash(), node2.ComputeHash(),
		"status and priority changes should not affect content hash")
}

// TestContentHash_NilFields_HandledCorrectly verifies nil/empty fields are handled.
func TestContentHash_NilFields_HandledCorrectly(t *testing.T) {
	tests := []struct {
		name string
		fn   func() string
	}{
		{
			"nil_labels",
			func() string { return model.ComputeContentHash("title", "", "", "", nil) },
		},
		{
			"empty_labels",
			func() string { return model.ComputeContentHash("title", "", "", "", []string{}) },
		},
	}

	// Nil and empty labels should produce the same hash.
	hash1 := tests[0].fn()
	hash2 := tests[1].fn()
	assert.Equal(t, hash1, hash2, "nil and empty labels should produce same hash")

	// All hashes should be valid SHA256.
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash := tt.fn()
			assert.Len(t, hash, 64, "hash should be 64 hex chars (SHA256)")
		})
	}
}

// TestContentHash_LabelsAreSorted verifies labels are sorted before hashing.
func TestContentHash_LabelsAreSorted(t *testing.T) {
	hash1 := model.ComputeContentHash("title", "", "", "", []string{"beta", "alpha", "gamma"})
	hash2 := model.ComputeContentHash("title", "", "", "", []string{"alpha", "beta", "gamma"})
	hash3 := model.ComputeContentHash("title", "", "", "", []string{"gamma", "beta", "alpha"})

	assert.Equal(t, hash1, hash2, "label order should not affect hash")
	assert.Equal(t, hash2, hash3, "label order should not affect hash")
}

// TestContentHash_DifferentLabels_DifferentHash verifies label content matters.
func TestContentHash_DifferentLabels_DifferentHash(t *testing.T) {
	hash1 := model.ComputeContentHash("title", "", "", "", []string{"a"})
	hash2 := model.ComputeContentHash("title", "", "", "", []string{"b"})

	assert.NotEqual(t, hash1, hash2, "different labels should produce different hash")
}

// TestContentHash_NodeMethod verifies the Node.ComputeHash() convenience method.
func TestContentHash_NodeMethod(t *testing.T) {
	node := &model.Node{
		Title:       "Test",
		Description: "desc",
		Prompt:      "prompt",
		Acceptance:  "criteria",
		Labels:      []string{"x"},
	}

	expected := model.ComputeContentHash("Test", "desc", "prompt", "criteria", []string{"x"})
	assert.Equal(t, expected, node.ComputeHash())
}

// TestContentHash_OriginalLabelsNotModified verifies sorting doesn't mutate input.
func TestContentHash_OriginalLabelsNotModified(t *testing.T) {
	labels := []string{"gamma", "alpha", "beta"}
	original := make([]string, len(labels))
	copy(original, labels)

	model.ComputeContentHash("title", "", "", "", labels)

	assert.Equal(t, original, labels, "original labels slice should not be modified")
}
