// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/oklog/ulid/v2"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// PromptService manages prompt editing and annotation operations per FR-3.4 and FR-12.1.
type PromptService struct {
	store       store.Store
	broadcaster EventBroadcaster
	logger      *slog.Logger
	clock       func() time.Time
}

// NewPromptService creates a PromptService with required dependencies.
func NewPromptService(
	s store.Store,
	broadcaster EventBroadcaster,
	logger *slog.Logger,
	clock func() time.Time,
) *PromptService {
	if broadcaster == nil {
		broadcaster = &NoopBroadcaster{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PromptService{
		store:       s,
		broadcaster: broadcaster,
		logger:      logger,
		clock:       clock,
	}
}

// UpdatePrompt updates a node's prompt field per FR-12.1.
// Recomputes content_hash (FR-3.7), sets updated_at, creates prompt_edit activity entry.
func (svc *PromptService) UpdatePrompt(ctx context.Context, nodeID, text, author string) error {
	node, err := svc.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node %s for prompt update: %w", nodeID, err)
	}

	oldHash := node.ContentHash

	// Update the prompt and recompute hash.
	node.Prompt = text
	node.UpdatedAt = svc.clock()
	newHash := node.ComputeHash()

	// Build update.
	promptPtr := &text
	hashPtr := &newHash
	updatedAt := node.UpdatedAt.UTC().Format(time.RFC3339)
	update := &store.NodeUpdate{
		Prompt:      promptPtr,
		ContentHash: hashPtr,
		UpdatedAt:   &updatedAt,
	}

	if err := svc.store.UpdateNode(ctx, nodeID, update); err != nil {
		return fmt.Errorf("update prompt for %s: %w", nodeID, err)
	}

	// Create activity entry with metadata.
	metadata, _ := json.Marshal(map[string]string{
		"old_prompt_hash": oldHash,
		"new_prompt_hash": newHash,
	})
	svc.broadcastPromptEvent(ctx, EventNodeUpdated, nodeID, author, metadata)

	return nil
}

// AddAnnotation appends an annotation to a node's annotations array per FR-3.4.
// Generates a ULID for sortability per FR-3.4.
func (svc *PromptService) AddAnnotation(ctx context.Context, nodeID, text, author string) error {
	node, err := svc.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node %s for annotation: %w", nodeID, err)
	}

	now := svc.clock()
	id, err := ulid.New(ulid.Timestamp(now), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate annotation ULID: %w", err)
	}

	annotation := model.Annotation{
		ID:        id.String(),
		Author:    author,
		Text:      text,
		CreatedAt: now,
		Resolved:  false,
	}

	annotations := make([]model.Annotation, 0, len(node.Annotations)+1)
	annotations = append(annotations, node.Annotations...)
	annotations = append(annotations, annotation)
	if err := svc.store.SetAnnotations(ctx, nodeID, annotations); err != nil {
		return fmt.Errorf("set annotations for %s: %w", nodeID, err)
	}

	svc.broadcastPromptEvent(ctx, EventNodeUpdated, nodeID, author, nil)
	return nil
}

// ResolveAnnotation sets the resolved flag on an annotation per FR-3.4.
// Returns ErrNotFound if the annotation ID does not exist on the node.
func (svc *PromptService) ResolveAnnotation(
	ctx context.Context, nodeID, annotationID string, resolved bool,
) error {
	node, err := svc.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node %s for resolve: %w", nodeID, err)
	}

	found := false
	for i := range node.Annotations {
		if node.Annotations[i].ID == annotationID {
			node.Annotations[i].Resolved = resolved
			found = true
			break
		}
	}

	if !found {
		return fmt.Errorf("annotation %s on node %s: %w",
			annotationID, nodeID, model.ErrNotFound)
	}

	if err := svc.store.SetAnnotations(ctx, nodeID, node.Annotations); err != nil {
		return fmt.Errorf("set annotations for %s: %w", nodeID, err)
	}

	svc.broadcastPromptEvent(ctx, EventNodeUpdated, nodeID, "", nil)
	return nil
}

// broadcastPromptEvent broadcasts a domain event for prompt/annotation operations.
func (svc *PromptService) broadcastPromptEvent(
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
		svc.logger.Error("failed to broadcast prompt event",
			"type", eventType, "node_id", nodeID, "error", err)
	}
}
