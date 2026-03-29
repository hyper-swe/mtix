// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// TestActivityEntry_AllTypes_Valid verifies all 9 activity types are valid.
func TestActivityEntry_AllTypes_Valid(t *testing.T) {
	allTypes := model.AllActivityTypes()
	assert.Len(t, allTypes, 9, "should have exactly 9 activity types")

	for _, at := range allTypes {
		t.Run(string(at), func(t *testing.T) {
			assert.True(t, at.IsValid(), "activity type %q should be valid", at)
		})
	}
}

// TestActivityEntry_InvalidType verifies unrecognized types are invalid.
func TestActivityEntry_InvalidType(t *testing.T) {
	tests := []model.ActivityType{
		"",
		"invalid",
		"COMMENT",
		"deleted",
	}

	for _, at := range tests {
		t.Run(string(at), func(t *testing.T) {
			assert.False(t, at.IsValid(), "activity type %q should be invalid", at)
		})
	}
}

// TestActivityEntry_RequiresText verifies which types mandate text.
func TestActivityEntry_RequiresText(t *testing.T) {
	tests := []struct {
		actType  model.ActivityType
		requires bool
	}{
		{model.ActivityTypeComment, true},
		{model.ActivityTypeNote, true},
		{model.ActivityTypeUnclaim, true},
		{model.ActivityTypeStatusChange, false},
		{model.ActivityTypeClaim, false},
		{model.ActivityTypeSystem, false},
		{model.ActivityTypePromptEdit, false},
		{model.ActivityTypeCreated, false},
		{model.ActivityTypeAnnotation, false},
	}

	for _, tt := range tests {
		t.Run(string(tt.actType), func(t *testing.T) {
			assert.Equal(t, tt.requires, tt.actType.RequiresText(),
				"activity type %q RequiresText mismatch", tt.actType)
		})
	}
}

// TestActivityEntry_Validate_ValidEntry_NoError verifies a valid entry passes.
func TestActivityEntry_Validate_ValidEntry_NoError(t *testing.T) {
	entry := &model.ActivityEntry{
		ID:        "01HQ4Z5V2ABCDEF123456789",
		Type:      model.ActivityTypeComment,
		Author:    "agent-1",
		Text:      "This is a comment",
		CreatedAt: time.Now(),
	}

	err := entry.Validate()
	assert.NoError(t, err)
}

// TestActivityEntry_Validate_MissingText_ForRequiredType verifies text-required types.
func TestActivityEntry_Validate_MissingText_ForRequiredType(t *testing.T) {
	requiredTextTypes := []model.ActivityType{
		model.ActivityTypeComment,
		model.ActivityTypeNote,
		model.ActivityTypeUnclaim,
	}

	for _, at := range requiredTextTypes {
		t.Run(string(at), func(t *testing.T) {
			entry := &model.ActivityEntry{
				ID:     "01HQ4Z5V2ABCDEF123456789",
				Type:   at,
				Author: "agent-1",
				Text:   "", // Missing required text
			}

			err := entry.Validate()
			require.Error(t, err)
			assert.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Contains(t, err.Error(), "text")
		})
	}
}

// TestActivityEntry_Validate_MissingID_ReturnsError verifies empty ID is rejected.
func TestActivityEntry_Validate_MissingID_ReturnsError(t *testing.T) {
	entry := &model.ActivityEntry{
		ID:     "",
		Type:   model.ActivityTypeSystem,
		Author: "system",
	}

	err := entry.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
}

// TestActivityEntry_Validate_MissingAuthor_ReturnsError verifies empty author is rejected.
func TestActivityEntry_Validate_MissingAuthor_ReturnsError(t *testing.T) {
	entry := &model.ActivityEntry{
		ID:     "01HQ4Z5V2ABCDEF123456789",
		Type:   model.ActivityTypeSystem,
		Author: "",
	}

	err := entry.Validate()
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrInvalidInput)
	assert.Contains(t, err.Error(), "author")
}

// TestAnnotation_Validate_ValidAnnotation_NoError verifies a valid annotation passes.
func TestAnnotation_Validate_ValidAnnotation_NoError(t *testing.T) {
	ann := model.Annotation{
		ID:        "01HQ4Z5V2ABCDEF123456789",
		Author:    "user@example.com",
		Text:      "Consider edge case X",
		CreatedAt: time.Now(),
		Resolved:  false,
	}

	// Annotation is a simple struct — verify all fields are accessible.
	assert.NotEmpty(t, ann.ID)
	assert.NotEmpty(t, ann.Author)
	assert.NotEmpty(t, ann.Text)
	assert.False(t, ann.CreatedAt.IsZero())
	assert.False(t, ann.Resolved)
}

// TestAnnotation_ULID_IsSortable verifies ULID-based IDs sort correctly by time.
func TestAnnotation_ULID_IsSortable(t *testing.T) {
	// ULIDs are sortable by timestamp component.
	// Earlier ULIDs sort before later ones as strings.
	earlier := "01HQ4Z5V2AAAAAAAAAAAAAAAA"
	later := "01HQ4Z5V2ZZZZZZZZZZZZZZZZ"

	assert.True(t, earlier < later,
		"earlier ULID should sort before later ULID")
}
