// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// ActivityType represents the type of an activity entry per FR-3.6.
type ActivityType string

const (
	// ActivityTypeComment is a human or agent comment.
	ActivityTypeComment ActivityType = "comment"

	// ActivityTypeStatusChange records a status transition.
	ActivityTypeStatusChange ActivityType = "status_change"

	// ActivityTypeNote is an LLM scratch note.
	ActivityTypeNote ActivityType = "note"

	// ActivityTypeAnnotation records a prompt annotation event.
	ActivityTypeAnnotation ActivityType = "annotation"

	// ActivityTypeUnclaim records when an agent releases work.
	ActivityTypeUnclaim ActivityType = "unclaim"

	// ActivityTypeClaim records when an agent claims work.
	ActivityTypeClaim ActivityType = "claim"

	// ActivityTypeSystem is a system-generated event.
	ActivityTypeSystem ActivityType = "system"

	// ActivityTypePromptEdit records a prompt modification.
	ActivityTypePromptEdit ActivityType = "prompt_edit"

	// ActivityTypeCreated records node creation.
	ActivityTypeCreated ActivityType = "created"
)

// AllActivityTypes returns all 9 valid activity entry types per FR-3.6.
func AllActivityTypes() []ActivityType {
	return []ActivityType{
		ActivityTypeComment,
		ActivityTypeStatusChange,
		ActivityTypeNote,
		ActivityTypeAnnotation,
		ActivityTypeUnclaim,
		ActivityTypeClaim,
		ActivityTypeSystem,
		ActivityTypePromptEdit,
		ActivityTypeCreated,
	}
}

// IsValid returns true if the activity type is recognized.
func (a ActivityType) IsValid() bool {
	switch a {
	case ActivityTypeComment, ActivityTypeStatusChange, ActivityTypeNote,
		ActivityTypeAnnotation, ActivityTypeUnclaim, ActivityTypeClaim,
		ActivityTypeSystem, ActivityTypePromptEdit, ActivityTypeCreated:
		return true
	default:
		return false
	}
}

// RequiresText returns true if the activity type mandates a non-empty text field.
func (a ActivityType) RequiresText() bool {
	switch a {
	case ActivityTypeComment, ActivityTypeNote, ActivityTypeUnclaim:
		return true
	default:
		return false
	}
}

// ActivityEntry represents a single event in a node's activity stream per FR-3.6.
type ActivityEntry struct {
	// ID is a ULID for sortability.
	ID string `json:"id"`

	// Type is the kind of activity entry.
	Type ActivityType `json:"type"`

	// Author is the agent ID or human email.
	Author string `json:"author"`

	// Text is the content (required for comment, note, unclaim; optional for others).
	Text string `json:"text,omitempty"`

	// CreatedAt is when the entry was created (UTC).
	CreatedAt time.Time `json:"created_at"`

	// Metadata contains optional additional information.
	// For status_change: {"from_status": "...", "to_status": "..."}
	Metadata json.RawMessage `json:"metadata,omitempty"`
}

// Validate checks the activity entry for correctness per FR-3.6.
func (a *ActivityEntry) Validate() error {
	if a.ID == "" {
		return fmt.Errorf("activity entry id is required: %w", ErrInvalidInput)
	}

	if !a.Type.IsValid() {
		return fmt.Errorf("invalid activity type %q: %w", a.Type, ErrInvalidInput)
	}

	if a.Author == "" {
		return fmt.Errorf("activity entry author is required: %w", ErrInvalidInput)
	}

	if a.Type.RequiresText() && a.Text == "" {
		return fmt.Errorf(
			"text is required for activity type %q: %w",
			a.Type, ErrInvalidInput,
		)
	}

	return nil
}
