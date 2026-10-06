// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// CreateNodeRequest contains the parameters for creating a new node.
type CreateNodeRequest struct {
	IssueType   model.IssueType `json:"issue_type,omitempty"`
	Assignee    string          `json:"assignee,omitempty"`
	ParentID    string          `json:"parent_id,omitempty"`
	Project     string          `json:"project"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Prompt      string          `json:"prompt,omitempty"`
	Acceptance  string          `json:"acceptance,omitempty"`
	Labels      []string        `json:"labels,omitempty"`
	Priority    model.Priority  `json:"priority,omitempty"`
	Creator     string          `json:"creator"`
	DeferUntil  *time.Time      `json:"defer_until,omitempty"`
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

	// settlement, when set, settles provisional nodes against the hub in the
	// background (ADR-003 §4 / MTIX-30.3). It is optional: nil means no hub is
	// configured, so creation keeps clean local numbers and never goes
	// provisional. settleWG tracks in-flight background settlements so
	// FlushSettlement can drain them on shutdown.
	settlement *SettlementService
	settleWG   sync.WaitGroup
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

// SetSettlement attaches the background settlement engine for distributed node
// identity (ADR-003 §4 / MTIX-30.3). It is wired once during process setup,
// before concurrent use. When unset (the single-user / no-hub case) creation
// keeps clean local numbers and never produces provisional ids.
func (svc *NodeService) SetSettlement(settlement *SettlementService) {
	svc.settlement = settlement
}

// FlushSettlement drains all in-flight background settlements and then runs one
// final synchronous settlement pass, so pending provisional nodes are settled
// before shutdown (ADR-003 §4 flush-on-shutdown). It is a safe no-op when no
// settlement engine is configured. It is the caller's join point on the
// background settlements CreateNode spawns.
func (svc *NodeService) FlushSettlement(ctx context.Context) {
	svc.settleWG.Wait()
	if svc.settlement.Enabled() {
		if _, _, err := svc.settlement.SettlePending(ctx); err != nil {
			svc.logger.Warn("settlement_flush_failed",
				"event", "settlement_flush_failed", "error", err)
		}
	}
}

// CreateNode creates a new node with validation, ID generation, and event broadcast.
// Implements FR-2.1a (project prefix validation), FR-3.1 (field validation),
// FR-3.9 (terminal parent rejection),
// FR-11.2a (auto-claim), and FR-2.7 (atomic sequence for ID generation).
//
// Distributed identity (ADR-003 §4 / MTIX-30.3): the trailing number is claimed
// eagerly and LOCALLY (never blocking on the network). When a hub is configured
// and reachable, the node is born with a clean number and its claim is confirmed
// against the hub by a BACKGROUND settlement (off this goroutine). When the hub
// is unreachable at create time, a child is born PROVISIONAL (a uid-bearing id)
// and re-settles on the next sync. The create call returns immediately either
// way — it never waits on the hub.
func (svc *NodeService) CreateNode(ctx context.Context, req *CreateNodeRequest) (*model.Node, error) {
	if err := svc.validateCreateRequest(req); err != nil {
		return nil, err
	}

	now := svc.clock()
	node, err := svc.buildNode(ctx, req, now)
	if err != nil {
		return nil, err
	}

	if err := svc.persistCreatedNode(ctx, node, req.Assignee); err != nil {
		return nil, fmt.Errorf("create node: %w", err)
	}
	svc.broadcastCreatedNode(ctx, node, req.Creator)

	// Eagerly settle against the hub in the BACKGROUND (ADR-003 §4): the create
	// call must not block on the network. An offline node simply stays
	// provisional and re-settles on the next sync.
	svc.scheduleSettlement()

	return node, nil
}

// persistCreatedNode allocates and optionally claims atomically (FR-2.7/FR-11.2a).
func (svc *NodeService) persistCreatedNode(ctx context.Context, node *model.Node, assignee string) error {
	return svc.store.CreateNodeAllocated(ctx, node, store.CreateNodeOptions{
		Assignee:            assignee,
		ClaimParentAssignee: svc.config.AutoClaim(),
		Provisional:         node.ParentID != "" && svc.settlement.Enabled() && !svc.settlement.Reachable(),
	})
}

// broadcastCreatedNode publishes creation and its committed initial claim in order.
func (svc *NodeService) broadcastCreatedNode(ctx context.Context, node *model.Node, creator string) {
	svc.broadcastEvent(ctx, EventNodeCreated, node.ID, creator, nil)
	if node.Assignee != "" {
		svc.broadcastEvent(ctx, EventNodeClaimed, node.ID, node.Assignee, nil)
	}
}

// scheduleSettlement kicks off one background settlement pass when a reachable
// hub is configured (ADR-003 §4 eager background settlement). It runs off the
// request goroutine so the create call never blocks on the hub; settleWG lets
// FlushSettlement join it on shutdown.
func (svc *NodeService) scheduleSettlement() {
	if !svc.settlement.Reachable() {
		return
	}
	svc.settleWG.Add(1)
	go func() {
		defer svc.settleWG.Done()
		if _, _, err := svc.settlement.SettlePending(context.Background()); err != nil {
			svc.logger.Warn("background_settlement_failed",
				"event", "background_settlement_failed", "error", err)
		}
	}()
}

// GetNode retrieves a node by ID, delegating to the store.
func (svc *NodeService) GetNode(ctx context.Context, id string) (*model.Node, error) {
	return svc.store.GetNode(ctx, id)
}

// UpdateNode applies partial updates with validation and event broadcast per FR-3.1.
// An omitted IssueType preserves classification; an empty value clears it.
func (svc *NodeService) UpdateNode(ctx context.Context, id string, updates *store.NodeUpdate) error {
	if updates.IssueType != nil {
		if err := model.ValidateIssueType(*updates.IssueType); err != nil {
			return fmt.Errorf("update issue type: %w", err)
		}
	}
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

// validateCreateRequest checks fields per FR-3.1 and the shared prefix grammar
// per FR-2.1a before sequence allocation or any persistence.
func (svc *NodeService) validateCreateRequest(req *CreateNodeRequest) error {
	if err := model.ValidateIssueType(req.IssueType); err != nil {
		return err
	}
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
	return model.ValidatePrefix(req.Project)
}

// buildNode constructs a model.Node from the CreateNodeRequest.
// Computes content hash (FR-3.7), UID and defaults before the allocation
// transaction (FR-2.7), preserving UID mint-before-write-lock timing.
func (svc *NodeService) buildNode(
	ctx context.Context, req *CreateNodeRequest, now time.Time,
) (*model.Node, error) {
	// The uid is the create-event id (ADR-003 §2), a UUIDv7 carrying the
	// time it is minted. Mint it with the creation time, before the steps
	// that can wait for the write lock (NextSequence), so its time stays
	// with created_at (MTIX-95.31.4).
	uid, err := clock.NewEventID()
	if err != nil {
		return nil, fmt.Errorf("generate uid: %w", err)
	}

	var parentID string
	var depth int

	if req.ParentID != "" {
		parentID = req.ParentID
		parent, getErr := svc.store.GetNode(ctx, parentID)
		if getErr != nil {
			return nil, fmt.Errorf("parent %s: %w", parentID, getErr)
		}
		// The display ID inherits the parent's actual prefix, even when a
		// caller supplies a different valid project (FR-2.1a / MTIX-107.3).
		// Historical sync/import prefixes remain readable; local child create
		// must validate this effective prefix before it consumes a sequence.
		if prefixErr := model.ValidatePrefix(model.ParseIDProject(parent.ID)); prefixErr != nil {
			return nil, fmt.Errorf("invalid inherited project prefix from parent %s; choose a parent with a valid project prefix: %w", parent.ID, prefixErr)
		}
		depth = parent.Depth + 1
	}

	priority := req.Priority
	if priority == 0 {
		priority = model.PriorityMedium
	}

	node := &model.Node{
		ID:          "",
		ParentID:    parentID,
		Project:     req.Project,
		Depth:       depth,
		Seq:         0,
		Title:       req.Title,
		IssueType:   req.IssueType,
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
		UID:         uid, // uid == create-event id (ADR-003 §2)
	}

	node.NodeType = model.NodeTypeForDepth(depth)
	node.ContentHash = node.ComputeHash()

	// FR-1.1a: Advisory depth warning (does NOT reject the operation).
	if depth > svc.config.MaxRecommendedDepth() {
		svc.logger.Warn("node exceeds recommended depth",
			"parent_id", parentID, "depth", depth, "max_recommended", svc.config.MaxRecommendedDepth())
	}

	return node, nil
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
