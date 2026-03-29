// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package testutil

import (
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// NodeOption is a functional option for configuring test nodes.
type NodeOption func(*model.Node)

// WithTitle sets the node's title.
func WithTitle(title string) NodeOption {
	return func(n *model.Node) {
		n.Title = title
	}
}

// WithParent sets the node's parent ID.
func WithParent(parentID string) NodeOption {
	return func(n *model.Node) {
		n.ParentID = parentID
	}
}

// WithStatus sets the node's status.
func WithStatus(status model.Status) NodeOption {
	return func(n *model.Node) {
		n.Status = status
	}
}

// WithPrompt sets the node's prompt.
func WithPrompt(prompt string) NodeOption {
	return func(n *model.Node) {
		n.Prompt = prompt
	}
}

// WithPriority sets the node's priority.
func WithPriority(priority model.Priority) NodeOption {
	return func(n *model.Node) {
		n.Priority = priority
	}
}

// WithDescription sets the node's description.
func WithDescription(desc string) NodeOption {
	return func(n *model.Node) {
		n.Description = desc
	}
}

// WithAcceptance sets the node's acceptance criteria.
func WithAcceptance(acceptance string) NodeOption {
	return func(n *model.Node) {
		n.Acceptance = acceptance
	}
}

// WithLabels sets the node's labels.
func WithLabels(labels ...string) NodeOption {
	return func(n *model.Node) {
		n.Labels = labels
	}
}

// FixedClock returns a clock function that always returns the given time.
// Use this in tests to ensure deterministic timestamps.
func FixedClock(t time.Time) func() time.Time {
	return func() time.Time {
		return t
	}
}

// MakeNode creates a test node with sensible defaults and applies options.
// It returns the node directly — it does NOT persist to the store.
// Use the service layer to persist (which generates IDs, validates, etc.).
func MakeNode(t *testing.T, opts ...NodeOption) *model.Node {
	t.Helper()

	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)

	node := &model.Node{
		Title:     "Test Node",
		Project:   "TEST",
		Status:    model.StatusOpen,
		Priority:  model.PriorityMedium,
		NodeType:  model.NodeTypeAuto,
		Weight:    1.0,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, opt := range opts {
		opt(node)
	}

	return node
}
