// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
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

	base := &CreateNodeRequest{ParentID: parentID, Project: parent.Project, Creator: creator}
	nodes, err := svc.prepareDecomposeChildren(ctx, base, children, svc.clock())
	if err != nil {
		return nil, err
	}
	if err := svc.store.CreateNodesAllocated(ctx, nodes, store.CreateNodeOptions{
		ClaimParentAssignee: svc.config.AutoClaim(),
		Provisional:         svc.settlement.Enabled() && !svc.settlement.Reachable(),
	}); err != nil {
		return nil, fmt.Errorf("create children under %s: %w", parentID, err)
	}
	createdIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		createdIDs = append(createdIDs, node.ID)
		svc.broadcastCreatedNode(ctx, node, creator)
	}

	// Broadcast progress.changed for the parent since children were added.
	svc.broadcastEvent(ctx, EventProgressChanged, parentID, creator, nil)

	return createdIDs, nil
}

// prepareDecomposeChildren applies child defaults and prepares every durable UID
// and content hash before the single storage transaction (FR-6.3 / ADR-003).
func (svc *NodeService) prepareDecomposeChildren(
	ctx context.Context, base *CreateNodeRequest, children []DecomposeInput, now time.Time,
) ([]*model.Node, error) {
	nodes := make([]*model.Node, 0, len(children))
	for _, child := range children {
		req := *base
		req.Title = child.Title
		req.Description = child.Description
		req.Prompt = child.Prompt
		req.Acceptance = child.Acceptance
		req.Priority = child.Priority
		req.Labels = child.Labels
		node, err := svc.buildNode(ctx, &req, now)
		if err != nil {
			return nil, fmt.Errorf("create child %q: %w", child.Title, err)
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}
