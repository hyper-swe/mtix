// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Restore transitions an invalidated node back to its previous_status per FR-3.5.
// Returns ErrInvalidTransition if the node is not in invalidated status.
// Clears invalidation fields (invalidated_at, invalidated_by, invalidation_reason).
func (svc *NodeService) Restore(ctx context.Context, nodeID, author string) error {
	node, err := svc.store.GetNode(ctx, nodeID)
	if err != nil {
		return fmt.Errorf("get node %s for restore: %w", nodeID, err)
	}

	if node.Status != model.StatusInvalidated {
		return fmt.Errorf(
			"cannot restore %s: current status is %s, not invalidated: %w",
			nodeID, node.Status, model.ErrInvalidTransition,
		)
	}

	// Determine restore target: previous_status or default to open.
	// If the previous_status transition is not valid (e.g., invalidated → done
	// is not in the state machine), fall back to open which is always valid
	// from invalidated per FR-3.5.
	restoreTo := model.StatusOpen
	if node.PreviousStatus != "" {
		if err := model.ValidateTransition(model.StatusInvalidated, node.PreviousStatus); err == nil {
			restoreTo = node.PreviousStatus
		}
		// Otherwise, fall back to StatusOpen.
	}

	// Use the store's TransitionStatus which handles activity recording.
	if err := svc.store.TransitionStatus(
		ctx, nodeID, restoreTo, "restored from invalidated", author,
	); err != nil {
		return fmt.Errorf("restore %s to %s: %w", nodeID, restoreTo, err)
	}

	svc.broadcastEvent(ctx, EventStatusChanged, nodeID, author, nil)
	return nil
}
