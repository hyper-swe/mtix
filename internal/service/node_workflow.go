// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// ClaimNode claims work atomically and broadcasts its successful change per FR-10.4.
func (svc *NodeService) ClaimNode(ctx context.Context, id, agent string) error {
	if err := svc.store.ClaimNode(ctx, id, agent); err != nil {
		return wrapAPIOperation(fmt.Sprintf("claim node %s", id), err)
	}
	svc.broadcastEvent(ctx, EventNodeClaimed, id, agent, nil)
	return nil
}

// ForceReclaimNode reclaims stale-agent work per FR-10.4a.
func (svc *NodeService) ForceReclaimNode(ctx context.Context, id, agent string, threshold time.Duration) error {
	if err := svc.store.ForceReclaimNode(ctx, id, agent, threshold); err != nil {
		return wrapAPIOperation(fmt.Sprintf("reclaim node %s", id), err)
	}
	svc.broadcastEvent(ctx, EventNodeClaimed, id, agent, nil)
	return nil
}

// UnclaimNode releases an assignment with its reason per FR-10.4.
func (svc *NodeService) UnclaimNode(ctx context.Context, id, reason, author string) error {
	if err := svc.store.UnclaimNode(ctx, id, reason, author); err != nil {
		return wrapAPIOperation(fmt.Sprintf("unclaim node %s", id), err)
	}
	svc.broadcastEvent(ctx, EventNodeUnclaimed, id, author, nil)
	return nil
}

// CancelNode preserves cancellation/cascade rules and broadcasts per FR-6.3/FR-7.5.
func (svc *NodeService) CancelNode(ctx context.Context, id, reason, author string, cascade bool) error {
	if err := svc.store.CancelNode(ctx, id, reason, author, cascade); err != nil {
		return wrapAPIOperation(fmt.Sprintf("cancel node %s", id), err)
	}
	svc.broadcastEvent(ctx, EventNodeCancelled, id, author, nil)
	return nil
}

// AppendAnnotation appends an existing annotation and broadcasts per FR-3.4/FR-19.1.
// The caller-supplied ID, clock and author remain unchanged in the HTTP response.
func (svc *NodeService) AppendAnnotation(ctx context.Context, id string, annotation model.Annotation) error {
	node, err := svc.store.GetNode(ctx, id)
	if err != nil {
		return wrapAPIOperation(fmt.Sprintf("get node %s for annotation", id), err)
	}
	annotations := append(append([]model.Annotation(nil), node.Annotations...), annotation)
	if err := svc.store.SetAnnotations(ctx, id, annotations); err != nil {
		return wrapAPIOperation(fmt.Sprintf("append annotation to %s", id), err)
	}
	svc.broadcastEvent(ctx, EventNodeUpdated, id, annotation.Author, nil)
	return nil
}
