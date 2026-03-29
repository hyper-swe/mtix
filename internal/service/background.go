// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// BackgroundService manages periodic cleanup and deferred node auto-wake
// per FR-3.3a (retention cleanup) and FR-3.8b (deferred auto-wake).
type BackgroundService struct {
	store  store.Store
	config ConfigProvider
	logger *slog.Logger
	clock  func() time.Time
}

// NewBackgroundService creates a BackgroundService with required dependencies.
func NewBackgroundService(
	s store.Store,
	config ConfigProvider,
	logger *slog.Logger,
	clock func() time.Time,
) *BackgroundService {
	if s == nil {
		panic("background service: store must not be nil")
	}
	if clock == nil {
		panic("background service: clock must not be nil")
	}
	if config == nil {
		config = &StaticConfig{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &BackgroundService{
		store:  s,
		config: config,
		logger: logger,
		clock:  clock,
	}
}

// RunScan performs one iteration of the background scan per FR-3.3a and FR-3.8b.
// This is called hourly by the serve command's background goroutine, or
// opportunistically by CLI write operations.
//
// 1. Permanently deletes soft-deleted nodes past retention period.
// 2. Auto-wakes deferred nodes whose defer_until has passed.
func (bg *BackgroundService) RunScan(ctx context.Context) error {
	cleanedCount, err := bg.cleanExpiredNodes(ctx)
	if err != nil {
		bg.logger.Error("retention cleanup failed", "error", err)
	} else if cleanedCount > 0 {
		bg.logger.Info("retention cleanup completed", "removed", cleanedCount)
	}

	wokenCount, err := bg.wakeDeferredNodes(ctx)
	if err != nil {
		bg.logger.Error("deferred wake failed", "error", err)
	} else if wokenCount > 0 {
		bg.logger.Info("deferred nodes auto-woken", "count", wokenCount)
	}

	return nil
}

// cleanExpiredNodes permanently deletes nodes whose deleted_at exceeds
// the soft_delete_retention period per FR-3.3a.
func (bg *BackgroundService) cleanExpiredNodes(ctx context.Context) (int, error) {
	retention := bg.config.SoftDeleteRetention()
	cutoff := bg.clock().Add(-retention).UTC().Format(time.RFC3339)

	// Query for expired soft-deleted nodes.
	rows, err := bg.store.Query(ctx,
		`SELECT id FROM nodes WHERE deleted_at IS NOT NULL AND deleted_at < ?`,
		cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("query expired nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			bg.logger.Error("failed to close rows", "error", closeErr)
		}
	}()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan expired node: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate expired nodes: %w", err)
	}

	// Permanently delete each expired node.
	for _, id := range ids {
		if err := bg.permanentlyDelete(ctx, id); err != nil {
			bg.logger.Error("failed to permanently delete node",
				"id", id, "error", err)
		}
	}

	return len(ids), nil
}

// permanentlyDelete removes a node from the database permanently per FR-3.3a.
func (bg *BackgroundService) permanentlyDelete(ctx context.Context, id string) error {
	db := bg.store.WriteDB()
	_, err := db.ExecContext(ctx,
		`DELETE FROM nodes WHERE id = ? AND deleted_at IS NOT NULL`,
		id,
	)
	if err != nil {
		return fmt.Errorf("permanent delete %s: %w", id, err)
	}
	return nil
}

// wakeDeferredNodes transitions deferred nodes whose defer_until has passed
// to open status per FR-3.8b.
func (bg *BackgroundService) wakeDeferredNodes(ctx context.Context) (int, error) {
	now := bg.clock().UTC().Format(time.RFC3339)

	rows, err := bg.store.Query(ctx,
		`SELECT id FROM nodes
		 WHERE status = ? AND defer_until IS NOT NULL AND defer_until < ?
		   AND deleted_at IS NULL`,
		string(model.StatusDeferred), now,
	)
	if err != nil {
		return 0, fmt.Errorf("query deferred nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			bg.logger.Error("failed to close rows", "error", closeErr)
		}
	}()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return 0, fmt.Errorf("scan deferred node: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate deferred nodes: %w", err)
	}

	// Transition each deferred node to open.
	for _, id := range ids {
		if err := bg.store.TransitionStatus(
			ctx, id, model.StatusOpen,
			"Auto-reopened: defer_until has passed", "system",
		); err != nil {
			bg.logger.Error("failed to wake deferred node",
				"id", id, "error", err)
		}
	}

	return len(ids), nil
}

// GetReadyNodes returns nodes available for agent pickup including
// past-due deferred nodes per FR-3.8b CLI behavior.
func (bg *BackgroundService) GetReadyNodes(ctx context.Context) ([]*model.Node, error) {
	now := bg.clock().UTC().Format(time.RFC3339)

	rows, err := bg.store.Query(ctx,
		`SELECT id FROM nodes
		 WHERE deleted_at IS NULL
		   AND assignee IS NULL
		   AND (
		     status = ?
		     OR (status = ? AND (defer_until IS NULL OR defer_until < ?))
		   )
		 ORDER BY priority ASC, created_at ASC`,
		string(model.StatusOpen), string(model.StatusDeferred), now,
	)
	if err != nil {
		return nil, fmt.Errorf("query ready nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			bg.logger.Error("failed to close rows", "error", closeErr)
		}
	}()

	var nodes []*model.Node
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan ready node: %w", err)
		}
		node, err := bg.store.GetNode(ctx, id)
		if err != nil {
			bg.logger.Error("failed to read ready node", "id", id, "error", err)
			continue
		}
		nodes = append(nodes, node)
	}

	return nodes, rows.Err()
}
