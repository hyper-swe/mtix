// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package model

import (
	"encoding/json"
	"fmt"
	"time"
)

// MaxTitleLength is the maximum allowed length for a node title per FR-3.1.
const MaxTitleLength = 500

// MaxDescriptionSize is the maximum allowed size for a description (50KB).
const MaxDescriptionSize = 50 * 1024

// MaxPromptSize is the maximum allowed size for a prompt (100KB).
const MaxPromptSize = 100 * 1024

// MaxRecommendedDepth is the advisory maximum depth per FR-1.1a.
const MaxRecommendedDepth = 50

// CodeRef represents a file/line/function reference per FR-3.2.
type CodeRef struct {
	File     string `json:"file"`
	Line     int    `json:"line,omitempty"`
	Function string `json:"function,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
}

// Node represents a single item in the mtix hierarchy per FR-3.1.
// All 38 stored fields from the requirements are represented here.
type Node struct {
	// Identity
	ID       string `json:"id"`
	ParentID string `json:"parent_id"`
	Project  string `json:"project"`
	Depth    int    `json:"depth"`
	Seq      int    `json:"seq"`

	// Content
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Prompt      string `json:"prompt,omitempty"`
	Acceptance  string `json:"acceptance,omitempty"`

	// Classification
	NodeType  NodeType  `json:"node_type"`
	IssueType IssueType `json:"issue_type,omitempty"`
	Priority  Priority  `json:"priority"`
	Labels    []string  `json:"labels,omitempty"`

	// State
	Status         Status  `json:"status"`
	Progress       float64 `json:"progress"`
	PreviousStatus Status  `json:"previous_status,omitempty"`

	// Assignment
	Assignee   string     `json:"assignee,omitempty"`
	Creator    string     `json:"creator,omitempty"`
	AgentState AgentState `json:"agent_state,omitempty"`

	// Timestamps
	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	ClosedAt   *time.Time `json:"closed_at,omitempty"`
	DeferUntil *time.Time `json:"defer_until,omitempty"`

	// Tracking
	EstimateMin *int    `json:"estimate_min,omitempty"`
	ActualMin   *int    `json:"actual_min,omitempty"`
	Weight      float64 `json:"weight"`
	ContentHash string  `json:"content_hash,omitempty"`

	// Code References
	CodeRefs   []CodeRef `json:"code_refs,omitempty"`
	CommitRefs []string  `json:"commit_refs,omitempty"`

	// Prompt Steering
	Annotations      []Annotation `json:"annotations,omitempty"`
	InvalidatedAt    *time.Time   `json:"invalidated_at,omitempty"`
	InvalidatedBy    string       `json:"invalidated_by,omitempty"`
	InvalidationReason string     `json:"invalidation_reason,omitempty"`

	// Soft-Delete
	DeletedAt *time.Time `json:"deleted_at,omitempty"`
	DeletedBy string     `json:"deleted_by,omitempty"`

	// Metadata
	Metadata  json.RawMessage `json:"metadata,omitempty"`
	SessionID string          `json:"session_id,omitempty"`
}

// Validate checks the node's fields for correctness per FR-3.1.
// Returns ErrInvalidInput with a descriptive message for any violation.
func (n *Node) Validate() error {
	if n.Title == "" {
		return fmt.Errorf("title is required: %w", ErrInvalidInput)
	}

	if len(n.Title) > MaxTitleLength {
		return fmt.Errorf(
			"title exceeds maximum length of %d characters: %w",
			MaxTitleLength, ErrInvalidInput,
		)
	}

	if len(n.Description) > MaxDescriptionSize {
		return fmt.Errorf(
			"description exceeds maximum size of %d bytes: %w",
			MaxDescriptionSize, ErrInvalidInput,
		)
	}

	if len(n.Prompt) > MaxPromptSize {
		return fmt.Errorf(
			"prompt exceeds maximum size of %d bytes: %w",
			MaxPromptSize, ErrInvalidInput,
		)
	}

	if n.Status != "" && !n.Status.IsValid() {
		return fmt.Errorf(
			"invalid status %q: %w",
			n.Status, ErrInvalidInput,
		)
	}

	if n.Priority != 0 && !n.Priority.IsValid() {
		return fmt.Errorf(
			"priority must be between %d and %d, got %d: %w",
			PriorityCritical, PriorityBacklog, n.Priority, ErrInvalidInput,
		)
	}

	return nil
}
