// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestBuildID_RootNode_PrefixDashSeq verifies root node ID format.
func TestBuildID_RootNode_PrefixDashSeq(t *testing.T) {
	id := model.BuildID("PROJ", "", 42)
	assert.Equal(t, "PROJ-42", id)
}

// TestBuildID_ChildNode_ParentDotSeq verifies child node ID format.
func TestBuildID_ChildNode_ParentDotSeq(t *testing.T) {
	id := model.BuildID("PROJ", "PROJ-42.1", 3)
	assert.Equal(t, "PROJ-42.1.3", id)
}

// TestBuildID_DeepNesting_CorrectFormat verifies deeply nested IDs.
func TestBuildID_DeepNesting_CorrectFormat(t *testing.T) {
	id := model.BuildID("PROJ", "PROJ-42.1.3.2.1", 4)
	assert.Equal(t, "PROJ-42.1.3.2.1.4", id)
}

// TestParseID_ExtractsProject verifies project extraction.
func TestParseID_ExtractsProject(t *testing.T) {
	tests := []struct {
		name    string
		id      string
		project string
	}{
		{"root", "PROJ-42", "PROJ"},
		{"child", "PROJ-42.1.3", "PROJ"},
		{"hyphenated_prefix", "MY-PROJECT-1.2", "MY"},
		{"no_dash", "INVALID", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.project, model.ParseIDProject(tt.id))
		})
	}
}

// TestParseID_ExtractsParentID verifies parent ID extraction.
func TestParseID_ExtractsParentID(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		parentID string
	}{
		{"root_has_no_parent", "PROJ-42", ""},
		{"depth_1", "PROJ-42.1", "PROJ-42"},
		{"depth_2", "PROJ-42.1.3", "PROJ-42.1"},
		{"depth_5", "PROJ-42.1.3.2.1.4", "PROJ-42.1.3.2.1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.parentID, model.ParseIDParent(tt.id))
		})
	}
}

// TestParseID_ExtractsDepth verifies depth calculation from ID.
func TestParseID_ExtractsDepth(t *testing.T) {
	tests := []struct {
		name  string
		id    string
		depth int
	}{
		{"root_depth_0", "PROJ-42", 0},
		{"epic_depth_1", "PROJ-42.1", 1},
		{"issue_depth_2", "PROJ-42.1.3", 2},
		{"micro_depth_3", "PROJ-42.1.3.2", 3},
		{"deep_depth_5", "PROJ-42.1.3.2.1.4", 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.depth, model.ParseIDDepth(tt.id))
		})
	}
}

// TestValidatePrefix_ValidPrefixes verifies valid project prefixes per FR-2.1a.
func TestValidatePrefix_ValidPrefixes(t *testing.T) {
	valid := []string{
		"A",
		"PROJ",
		"MY-PROJECT",
		"TEST",
		"MTIX",
		"A123",
		"A-B-C",
		"ABCDEFGHIJKLMNOPQRST", // exactly 20 chars
	}

	for _, prefix := range valid {
		t.Run(prefix, func(t *testing.T) {
			err := model.ValidatePrefix(prefix)
			assert.NoError(t, err, "prefix %q should be valid", prefix)
		})
	}
}

// TestValidatePrefix_InvalidPrefixes verifies invalid project prefixes per FR-2.1a.
func TestValidatePrefix_InvalidPrefixes(t *testing.T) {
	tests := []struct {
		name   string
		prefix string
	}{
		{"empty", ""},
		{"lowercase", "proj"},
		{"starts_with_number", "1PROJ"},
		{"starts_with_hyphen", "-PROJ"},
		{"contains_underscore", "MY_PROJ"},
		{"contains_percent", "MY%PROJ"},
		{"contains_space", "MY PROJ"},
		{"too_long", "ABCDEFGHIJKLMNOPQRSTU"}, // 21 chars
		{"special_chars", "PROJ!@#"},
		{"sql_wildcard_percent", "PROJ%"},
		{"sql_wildcard_underscore", "PROJ_1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := model.ValidatePrefix(tt.prefix)
			require.Error(t, err, "prefix %q should be invalid", tt.prefix)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
		})
	}
}

// TestSequenceKey_Format verifies the sequence key format.
func TestSequenceKey_Format(t *testing.T) {
	tests := []struct {
		name     string
		project  string
		parentID string
		expected string
	}{
		{"root", "PROJ", "", "PROJ:"},
		{"child", "PROJ", "PROJ-42", "PROJ:PROJ-42"},
		{"nested", "PROJ", "PROJ-42.1.3", "PROJ:PROJ-42.1.3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, model.SequenceKey(tt.project, tt.parentID))
		})
	}
}
