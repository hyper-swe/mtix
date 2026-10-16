// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// Generate once during preparation; the accepted canonical batch retains this
// publication identity independently of host-local caller/replay associations.
func (s *WakePermissions) newPermissionPublicationID(ctx context.Context) (string, error) {
	for range 4 {
		id, err := uuid.NewV7()
		if err != nil {
			return "", fmt.Errorf("create permission publication identity: %w", err)
		}
		exists, err := s.Store.PermissionPublicationExists(ctx, id.String())
		if err != nil {
			return "", fmt.Errorf("check permission publication identity: %w", err)
		}
		if !exists {
			return id.String(), nil
		}
	}
	return "", fmt.Errorf("permission publication identity collision: %w", model.ErrConflict)
}

func permissionPublicationID(batch oplocal.PermissionBatch) string {
	if batch.PublicationID != "" {
		return batch.PublicationID
	}
	// Previously accepted batches must retry their original event, never duplicate it.
	return batch.ID
}

// ApplyFileBatch owns file-mode policy rather than trusting caller mode labels.
func (s *WakePermissions) ApplyFileBatch(ctx context.Context, board PermissionBoard, batch oplocal.PermissionBatch) error {
	batch.Mode = "file"
	return s.ApplyBatch(ctx, board, batch)
}

func (s *WakePermissions) permissionNoticeUnavailable(ctx context.Context, batch oplocal.PermissionBatch) (bool, error) {
	node, err := s.Store.GetNode(ctx, batch.NodeID)
	if errors.Is(err, model.ErrNotFound) {
		return true, nil
	}
	if err != nil {
		return false, fmt.Errorf("inspect permission notice target: %w", err)
	}
	return node.DeletedAt != nil, nil
}
