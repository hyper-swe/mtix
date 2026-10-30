// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// LiveNodeIDByUID returns the current id of the live (not deleted) task that
// holds uid. A task keeps its uid when it is renumbered, so this recognizes a
// task that has already moved (MTIX-107.46). Returns ErrNotFound when no live
// task holds the uid.
func (s *Store) LiveNodeIDByUID(ctx context.Context, uid string) (string, error) {
	var id string
	// uid is unique among nodes (idx_nodes_uid, a partial index that needs
	// uid <> '' in the query to be used); a soft-deleted task is not live.
	err := s.readDB.QueryRowContext(ctx,
		`SELECT id FROM nodes WHERE uid = ? AND uid <> '' AND deleted_at IS NULL`, uid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("task with uid: %w", model.ErrNotFound)
	}
	if err != nil {
		return "", fmt.Errorf("look up task by uid: %w", err)
	}
	return id, nil
}
