// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// CreateNodeRequest contains the parameters for creating a new node.
type CreateNodeRequest struct {
	ParentID    string         `json:"parent_id,omitempty"`
	Project     string         `json:"project"`
	Title       string         `json:"title"`
	Description string         `json:"description,omitempty"`
	Prompt      string         `json:"prompt,omitempty"`
	Acceptance  string         `json:"acceptance,omitempty"`
	Labels      []string       `json:"labels,omitempty"`
	Priority    model.Priority `json:"priority,omitempty"`
	Creator     string         `json:"creator"`
	DeferUntil  *time.Time     `json:"defer_until,omitempty"`
}

// NodeService orchestrates node business logic per MTIX-3.1.1.
// It enforces validation, state machine rules, event broadcasting,
// and delegates data access to the Store interface.
type NodeService struct {
	store       store.Store
	broadcaster EventBroadcaster
	config      ConfigProvider
	logger      *slog.Logger
	clock       func() time.Time
}

// NewNodeService creates a NodeService with all required dependencies.
// Panics if store or clock is nil — these are programming errors, not runtime conditions.
func NewNodeService(
	s store.Store,
	broadcaster EventBroadcaster,
	config ConfigProvider,
	logger *slog.Logger,
	clock func() time.Time,
) *NodeService {
	if s == nil {
		panic("node service: store must not be nil")
	}
	if clock == nil {
		panic("node service: clock must not be nil")
	}
	if broadcaster == nil {
		broadcaster = &NoopBroadcaster{}
	}
	if config == nil {
		config = &StaticConfig{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &NodeService{
		store:       s,
		broadcaster: broadcaster,
		config:      config,
		logger:      logger,
		clock:       clock,
	}
}

// CreateNode creates a new node with validation, ID generation, and event broadcast.
// Implements FR-3.1 (field validation), FR-3.9 (terminal parent rejection),
// FR-11.2a (auto-claim), and FR-2.7 (atomic sequence for ID generation).
func (svc *NodeService) CreateNode(ctx context.Context, req *CreateNodeRequest) (*model.Node, error) {
	if err := svc.validateCreateRequest(req); err != nil {
		return nil, err
	}

	now := svc.clock()
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

// GetNode retrieves a node by ID, delegating to the store.
func (svc *NodeService) GetNode(ctx context.Context, id string) (*model.Node, error) {
	return svc.store.GetNode(ctx, id)
}

// UpdateNode applies partial updates to a node with validation and event broadcast.
func (svc *NodeService) UpdateNode(ctx context.Context, id string, updates *store.NodeUpdate) error {
	// Validate title length if being updated.
	if updates.Title != nil {
		if *updates.Title == "" {
			return fmt.Errorf("title cannot be empty: %w", model.ErrInvalidInput)
		}
		if len(*updates.Title) > model.MaxTitleLength {
			return fmt.Errorf("title exceeds %d characters: %w",
				model.MaxTitleLength, model.ErrInvalidInput)
		}
	}

	if err := svc.store.UpdateNode(ctx, id, updates); err != nil {
		return fmt.Errorf("update node %s: %w", id, err)
	}

	svc.broadcastEvent(ctx, EventNodeUpdated, id, "", nil)
	return nil
}

// DeleteNode soft-deletes a node with optional cascade and event broadcast.
func (svc *NodeService) DeleteNode(ctx context.Context, id string, cascade bool, deletedBy string) error {
	if err := svc.store.DeleteNode(ctx, id, cascade, deletedBy); err != nil {
		return fmt.Errorf("delete node %s: %w", id, err)
	}

	svc.broadcastEvent(ctx, EventNodeDeleted, id, deletedBy, nil)
	return nil
}

// UndeleteNode restores a soft-deleted node.
func (svc *NodeService) UndeleteNode(ctx context.Context, id string) error {
	if err := svc.store.UndeleteNode(ctx, id); err != nil {
		return fmt.Errorf("undelete node %s: %w", id, err)
	}

	svc.broadcastEvent(ctx, EventNodeUndeleted, id, "", nil)
	return nil
}

// TransitionStatus validates and applies a status transition per FR-3.5.
// Validates the transition before delegating to the store.
func (svc *NodeService) TransitionStatus(
	ctx context.Context, id string, toStatus model.Status, reason, author string,
) error {
	// Read current status to validate the transition.
	node, err := svc.store.GetNode(ctx, id)
	if err != nil {
		return fmt.Errorf("get node for transition: %w", err)
	}

	if err := model.ValidateTransition(node.Status, toStatus); err != nil {
		return err
	}

	if err := svc.store.TransitionStatus(ctx, id, toStatus, reason, author); err != nil {
		return fmt.Errorf("transition %s to %s: %w", id, toStatus, err)
	}

	svc.broadcastEvent(ctx, EventStatusChanged, id, author, nil)
	return nil
}

// validateCreateRequest checks the CreateNodeRequest for correctness per FR-3.1.
func (svc *NodeService) validateCreateRequest(req *CreateNodeRequest) error {
	if req.Title == "" {
		return fmt.Errorf("title is required: %w", model.ErrInvalidInput)
	}
	if len(req.Title) > model.MaxTitleLength {
		return fmt.Errorf("title exceeds maximum length of %d characters: %w",
			model.MaxTitleLength, model.ErrInvalidInput)
	}
	if len(req.Description) > model.MaxDescriptionSize {
		return fmt.Errorf("description exceeds maximum size: %w", model.ErrInvalidInput)
	}
	if len(req.Prompt) > model.MaxPromptSize {
		return fmt.Errorf("prompt exceeds maximum size: %w", model.ErrInvalidInput)
	}
	if req.Project == "" {
		return fmt.Errorf("project is required: %w", model.ErrInvalidInput)
	}
	return nil
}

// buildNode constructs a model.Node from the CreateNodeRequest.
// Generates the dot-notation ID via atomic sequence (FR-2.7),
// computes content hash (FR-3.7), and sets defaults.
func (svc *NodeService) buildNode(
	ctx context.Context, req *CreateNodeRequest, now time.Time,
) (*model.Node, error) {
	var parentID string
	var depth int
	var seqKey string

	if req.ParentID != "" {
		parentID = req.ParentID
		parent, err := svc.store.GetNode(ctx, parentID)
		if err != nil {
			return nil, fmt.Errorf("parent %s: %w", parentID, err)
		}
		depth = parent.Depth + 1
		seqKey = req.Project + ":" + parentID
	} else {
		seqKey = req.Project + ":"
	}

	seq, err := svc.store.NextSequence(ctx, seqKey)
	if err != nil {
		return nil, fmt.Errorf("generate sequence: %w", err)
	}

	id := model.BuildID(req.Project, parentID, seq)

	priority := req.Priority
	if priority == 0 {
		priority = model.PriorityMedium
	}

	node := &model.Node{
		ID:          id,
		ParentID:    parentID,
		Project:     req.Project,
		Depth:       depth,
		Seq:         seq,
		Title:       req.Title,
		Description: req.Description,
		Prompt:      req.Prompt,
		Acceptance:  req.Acceptance,
		Labels:      req.Labels,
		Priority:    priority,
		Status:      model.StatusOpen,
		Creator:     req.Creator,
		Weight:      1.0,
		CreatedAt:   now,
		UpdatedAt:   now,
		DeferUntil:  req.DeferUntil,
	}

	node.NodeType = model.NodeTypeForDepth(depth)
	node.ContentHash = node.ComputeHash()

	// FR-1.1a: Advisory depth warning (does NOT reject the operation).
	if depth > svc.config.MaxRecommendedDepth() {
		svc.logger.Warn("node exceeds recommended depth",
			"id", id, "depth", depth, "max_recommended", svc.config.MaxRecommendedDepth())
	}

	return node, nil
}

// maybeAutoClaim implements FR-11.2a: auto-claim child when configured
// and parent is in_progress with an assignee.
func (svc *NodeService) maybeAutoClaim(
	ctx context.Context, node *model.Node, req *CreateNodeRequest,
) error {
	if !svc.config.AutoClaim() || req.ParentID == "" {
		return nil
	}

	parent, err := svc.store.GetNode(ctx, req.ParentID)
	if err != nil {
		return fmt.Errorf("read parent for auto-claim: %w", err)
	}

	if parent.Status != model.StatusInProgress || parent.Assignee == "" {
		return nil
	}

	// Auto-claim the child for the parent's assignee.
	if err := svc.store.ClaimNode(ctx, node.ID, parent.Assignee); err != nil {
		return fmt.Errorf("auto-claim %s for %s: %w", node.ID, parent.Assignee, err)
	}

	svc.broadcastEvent(ctx, EventNodeClaimed, node.ID, parent.Assignee, nil)
	return nil
}

// broadcastEvent is a helper that logs and broadcasts an event.
// It never returns an error — broadcast failures are logged but do not fail operations.
func (svc *NodeService) broadcastEvent(
	ctx context.Context, eventType EventType, nodeID, author string, data json.RawMessage,
) {
	event := Event{
		Type:      eventType,
		NodeID:    nodeID,
		Timestamp: svc.clock(),
		Author:    author,
		Data:      data,
	}
	if err := svc.broadcaster.Broadcast(ctx, event); err != nil {
		svc.logger.Error("failed to broadcast event",
			"type", eventType, "node_id", nodeID, "error", err)
	}
}
