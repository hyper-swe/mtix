// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
)

// Restore transitions an invalidated node back to its previous_status per FR-3.5.
// The status check and the restore target are decided inside the store's write
// transaction (MTIX-151), so a concurrent cancel or restore cannot be
// overwritten by a stale read.
// Returns ErrInvalidTransition if the node is not in invalidated status.
// Clears invalidation fields (invalidated_at, invalidated_by, invalidation_reason).
// If previous_status is not reachable from invalidated (e.g., invalidated → done
// is not in the state machine), the node returns to open, which is always valid
// from invalidated per FR-3.5.
func (svc *NodeService) Restore(ctx context.Context, nodeID, author string) error {
	_, err := svc.store.RestoreInvalidated(ctx, nodeID, author)
	if err != nil {
		return fmt.Errorf("restore %s: %w", nodeID, err)
	}

	svc.broadcastEvent(ctx, EventStatusChanged, nodeID, author, nil)
	return nil
}
