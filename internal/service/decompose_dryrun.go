// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// ProposedNode represents a node that would be created by a decompose operation
// but has not been persisted. Used by dry-run to preview the proposed tree.
type ProposedNode struct {
	ID          string         `json:"id"`
	ParentID    string         `json:"parent_id"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Prompt      string         `json:"prompt,omitempty"`
	Acceptance  string         `json:"acceptance,omitempty"`
	Priority    model.Priority `json:"priority,omitempty"`
	Labels      []string       `json:"labels,omitempty"`
}

// DecomposeDryRun validates the decompose inputs and computes the proposed
// child nodes without writing anything to the store. It performs all the
// same validation as Decompose (parent exists, parent not terminal, titles
// valid) but uses GetDirectChildren to count existing siblings instead of
// calling NextSequence, which would mutate the sequence counter.
//
// Returns ErrNotFound if the parent does not exist.
// Returns ErrInvalidInput if the parent is terminal, children are empty,
// or any child has an invalid title.
func (svc *NodeService) DecomposeDryRun(
	ctx context.Context, parentID string, children []DecomposeInput,
) ([]ProposedNode, error) {
	if len(children) == 0 {
		return nil, fmt.Errorf("at least one child is required: %w", model.ErrInvalidInput)
	}

	// Validate all children upfront.
	for i, child := range children {
		if child.Title == "" {
			return nil, fmt.Errorf("child %d: title is required: %w", i, model.ErrInvalidInput)
		}
		if len(child.Title) > model.MaxTitleLength {
			return nil, fmt.Errorf("child %d: title too long: %w", i, model.ErrInvalidInput)
		}
	}

	// Verify parent exists and is not in terminal status (FR-3.9).
	parent, err := svc.store.GetNode(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("get parent %s: %w", parentID, err)
	}
	if parent.Status.IsTerminal() {
		return nil, fmt.Errorf(
			"cannot decompose under %s parent %s: %w",
			parent.Status, parentID, model.ErrInvalidInput,
		)
	}

	// Count existing children to compute proposed IDs without mutating sequences.
	existing, err := svc.store.GetDirectChildren(ctx, parentID)
	if err != nil {
		return nil, fmt.Errorf("get existing children of %s: %w", parentID, err)
	}
	nextSeq := len(existing) + 1

	proposed := make([]ProposedNode, len(children))
	for i, child := range children {
		proposed[i] = ProposedNode{
			ID:          model.BuildID(parent.Project, parentID, nextSeq+i),
			ParentID:    parentID,
			Title:       child.Title,
			Description: child.Description,
			Prompt:      child.Prompt,
			Acceptance:  child.Acceptance,
			Priority:    child.Priority,
			Labels:      child.Labels,
		}
	}

	return proposed, nil
}
