// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

func permissionRandomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("create permission occurrence identity: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func (s *WakePermissions) permissionOccurrence(ctx context.Context, batch oplocal.PermissionBatch) (oplocal.PermissionBatch, error) {
	if batch.ID == "" {
		return batch, nil
	}
	for range 4 {
		exists, err := s.Store.PermissionPublicationExists(ctx, batch.ID)
		if err != nil || !exists {
			return batch, err
		}
		if batch.CallerID == "" {
			batch.CallerID = batch.ID
		}
		batch.ID, err = permissionRandomID()
		if err != nil {
			return batch, err
		}
	}
	return batch, fmt.Errorf("permission occurrence identity collision: %w", model.ErrConflict)
}

func (s *WakePermissions) permissionPublisher(board string) func(context.Context, oplocal.PermissionBatch) error {
	return func(ctx context.Context, batch oplocal.PermissionBatch) error {
		if batch.BoardID != board {
			return fmt.Errorf("pending permission publication belongs to another board: %w", model.ErrConflict)
		}
		return s.publishPermissionBatch(ctx, batch)
	}
}
