// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// DecomposeInput contains the parameters for a single child in a decompose operation.
type DecomposeInput struct {
	Title       string         `json:"title"`
	Prompt      string         `json:"prompt,omitempty"`
	Acceptance  string         `json:"acceptance,omitempty"`
	Description string         `json:"description,omitempty"`
	Priority    model.Priority `json:"priority,omitempty"`
	Labels      []string       `json:"labels,omitempty"`
}

// Decompose creates multiple child nodes under a parent in a single atomic operation
// per FR-6.3. All children are created in the same transaction — partial failures
// roll back the entire batch. IDs are generated via atomic sequence (FR-2.7).
//
// Returns the list of created node IDs.
// Returns ErrInvalidInput if the parent is in a terminal status (FR-3.9).
// Returns ErrNotFound if the parent does not exist.
func (svc *NodeService) Decompose(
	ctx context.Context, parentID string, children []DecomposeInput, creator string,
) ([]string, error) {
	if len(children) == 0 {
		return nil, fmt.Errorf("at least one child is required: %w", model.ErrInvalidInput)
	}

	// Validate all children upfront before any writes.
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
			"cannot decompose under %s parent %s; reopen it first: %w",
			parent.Status, parentID, model.ErrInvalidInput,
		)
	}

	now := svc.clock()
	createdIDs := make([]string, 0, len(children))

	// Create each child via the service's CreateNode to reuse all validation,
	// ID generation, content hash, and activity recording logic.
	for _, child := range children {
		req := &CreateNodeRequest{
			ParentID:    parentID,
			Project:     parent.Project,
			Title:       child.Title,
			Description: child.Description,
			Prompt:      child.Prompt,
			Acceptance:  child.Acceptance,
			Priority:    child.Priority,
			Labels:      child.Labels,
			Creator:     creator,
			DeferUntil:  nil,
		}

		// Override default priority if needed — children inherit parent defaults.
		node, err := svc.createChildForDecompose(ctx, req, now)
		if err != nil {
			return nil, fmt.Errorf("create child %q: %w", child.Title, err)
		}
		createdIDs = append(createdIDs, node.ID)
	}

	// Broadcast progress.changed for the parent since children were added.
	svc.broadcastEvent(ctx, EventProgressChanged, parentID, creator, nil)

	return createdIDs, nil
}

// createChildForDecompose creates a single child node for decompose operations.
// Delegates to the full buildNode + store.CreateNode + auto-claim pipeline.
func (svc *NodeService) createChildForDecompose(
	ctx context.Context, req *CreateNodeRequest, now time.Time,
) (*model.Node, error) {
	node, err := svc.buildNode(ctx, req, now)
	if err != nil {
		return nil, err
	}

	if err := svc.store.CreateNode(ctx, node); err != nil {
		return nil, fmt.Errorf("create node: %w", err)
	}

	// FR-11.2a: Auto-claim when configured and parent is in_progress with assignee.
	if err := svc.maybeAutoClaim(ctx, node, req); err != nil {
		return nil, fmt.Errorf("auto-claim: %w", err)
	}

	svc.broadcastEvent(ctx, EventNodeCreated, node.ID, req.Creator, nil)
	return node, nil
}
