// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// PromptService manages prompt editing and annotation operations per FR-3.4 and FR-12.1.
type PromptService struct {
	store       store.Store
	broadcaster EventBroadcaster
	logger      *slog.Logger
	clock       func() time.Time
	ids         *annotationIDs
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
		ids:         newAnnotationIDs(rand.Reader),
	}
}

// UpdatePrompt updates a node's prompt field per FR-12.1.
// The store commits the prompt and its hashes atomically (FR-3.7), then the
// service broadcasts that transaction-specific pair (FR-7.5).
func (svc *PromptService) UpdatePrompt(ctx context.Context, nodeID, text, author string) error {
	oldHash, newHash, err := svc.store.UpdatePrompt(ctx, nodeID, text)
	if err != nil {
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

// mentionPattern matches the first @<agent> mention token in comment text
// (FR-19.1). The captured agent id follows the FR-18.7 author_id grammar
// (^[a-z0-9_-]{1,64}$, matching model.authorIDPattern). The @ must sit at a
// token boundary — preceded by start-of-text or a non-word, non-@ character —
// so an email-like "foo@bar" is not misread as a mention.
var mentionPattern = regexp.MustCompile(`(?:^|[^\w@])@([a-z0-9_-]{1,64})`)

// ParseAddressee extracts the addressee agent id from the FIRST @<agent> token
// in comment text per FR-19.1, or "" when the text carries no mention. The
// token is NOT stripped — it stays visible in the stored text and merely also
// sets the addressee. Only the first mention is honored: the addressee is
// single-valued because the inbox keys on one `to`.
func ParseAddressee(text string) string {
	m := mentionPattern.FindStringSubmatch(text)
	if m == nil {
		return ""
	}
	return m[1]
}

// AddAnnotation appends an annotation to a node's annotations array per FR-3.4.
// Generates a ULID for sortability per FR-3.4.
//
// When addressee is empty, the first @<agent> token in text is used as the
// addressee (FR-19.1); an explicit addressee arg takes precedence over a token.
func (svc *PromptService) AddAnnotation(ctx context.Context, nodeID, text, author, addressee string) error {
	if addressee == "" {
		addressee = ParseAddressee(text)
	}

	now := svc.clock()
	id, err := svc.ids.next(now)
	if err != nil {
		return err
	}

	annotation := model.Annotation{
		ID:        id,
		Author:    author,
		Text:      text,
		CreatedAt: now,
		Resolved:  false,
		Addressee: addressee,
		Hop:       addressedCommentHop(addressee, svc.logger),
	}

	// One atomic store append: no read-modify-write outside a transaction (MTIX-148).
	if err := svc.store.AppendAnnotation(ctx, nodeID, annotation); err != nil {
		return fmt.Errorf("append annotation to %s: %w", nodeID, err)
	}

	svc.broadcastPromptEvent(ctx, EventNodeUpdated, nodeID, author, nil)
	return nil
}

// ResolveAnnotation sets the resolved flag on an annotation per FR-3.4.
// The annotation is named by its full id or by an unambiguous prefix (see
// ResolveAnnotationRef). Returns ErrNotFound if no annotation matches and
// ErrInvalidInput if the reference is too short or ambiguous.
func (svc *PromptService) ResolveAnnotation(
	ctx context.Context, nodeID, annotationRef string, resolved bool,
) error {
	_, err := svc.ResolveAnnotationRef(ctx, nodeID, annotationRef, resolved)
	return err
}

// ResolveAnnotationRef is ResolveAnnotation that also returns the full id of
// the annotation it changed, so callers can report it (FR-3.4, MTIX-100.3).
// A full id is an exact match and always wins. Otherwise the reference is a
// prefix of at least model.AnnotationShortIDLen characters, the length
// `mtix show` prints. Several matches are refused with ErrInvalidInput that
// lists the full ids; none is ErrNotFound. Every id in an error is rendered
// with format.EventID.
func (svc *PromptService) ResolveAnnotationRef(
	ctx context.Context, nodeID, annotationRef string, resolved bool,
) (string, error) {
	node, err := svc.store.GetNode(ctx, nodeID)
	if err != nil {
		return "", fmt.Errorf("resolve annotation %s on node %s: %w", format.EventID(annotationRef), nodeID, err)
	}
	annotationID, err := matchAnnotationRef(node.Annotations, annotationRef)
	if err != nil {
		return "", fmt.Errorf("resolve annotation on node %s: %w", nodeID, err)
	}
	// One atomic store update by the full annotation ID (MTIX-148).
	if err := svc.store.ResolveAnnotation(ctx, nodeID, annotationID, resolved); err != nil {
		return "", fmt.Errorf("resolve annotation %s on node %s: %w", format.EventID(annotationID), nodeID, err)
	}

	svc.broadcastPromptEvent(ctx, EventNodeUpdated, nodeID, "", nil)
	return annotationID, nil
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
