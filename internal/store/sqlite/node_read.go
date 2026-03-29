// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// GetNode retrieves a node by its dot-notation ID.
// Excludes soft-deleted nodes (deleted_at IS NULL) per FR-3.3.
// Returns ErrNotFound if the node does not exist or is soft-deleted.
func (s *Store) GetNode(ctx context.Context, id string) (*model.Node, error) {
	// Query with parameterized ID — no string concatenation.
	row := s.readDB.QueryRowContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		id,
	)

	node, err := scanNode(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("node %s: %w", id, model.ErrNotFound)
		}
		return nil, fmt.Errorf("get node %s: %w", id, err)
	}

	return node, nil
}

// GetDirectChildren returns all direct, non-deleted children of a node.
// Ordered by sequence number for deterministic results.
func (s *Store) GetDirectChildren(ctx context.Context, parentID string) ([]*model.Node, error) {
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes
		 WHERE parent_id = ? AND deleted_at IS NULL
		 ORDER BY seq ASC`,
		parentID,
	)
	if err != nil {
		return nil, fmt.Errorf("query children of %s: %w", parentID, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close rows", "error", closeErr)
		}
	}()

	var children []*model.Node
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan child of %s: %w", parentID, err)
		}
		children = append(children, node)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate children of %s: %w", parentID, err)
	}

	return children, nil
}

// GetActivity returns activity entries for a node with pagination per FR-3.6.
// Reads the JSON activity column and applies limit/offset in Go.
// Returns ErrNotFound if the node does not exist or is soft-deleted.
func (s *Store) GetActivity(ctx context.Context, nodeID string, limit, offset int) ([]model.ActivityEntry, error) {
	var activityJSON sql.NullString
	err := s.readDB.QueryRowContext(ctx,
		`SELECT activity FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		nodeID,
	).Scan(&activityJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("node %s: %w", nodeID, model.ErrNotFound)
		}
		return nil, fmt.Errorf("get activity for %s: %w", nodeID, err)
	}

	var entries []model.ActivityEntry
	if activityJSON.Valid && activityJSON.String != "" && activityJSON.String != "[]" {
		if unmarshalErr := json.Unmarshal([]byte(activityJSON.String), &entries); unmarshalErr != nil {
			return nil, fmt.Errorf("unmarshal activity for %s: %w", nodeID, unmarshalErr)
		}
	}

	// Apply offset.
	if offset > 0 && offset < len(entries) {
		entries = entries[offset:]
	} else if offset >= len(entries) {
		return []model.ActivityEntry{}, nil
	}

	// Apply limit.
	if limit > 0 && limit < len(entries) {
		entries = entries[:limit]
	}

	return entries, nil
}

// childCount returns the number of direct non-deleted children for a node.
// This is computed at query time per FR-3.1 (child_count is NOT stored).
func (s *Store) childCount(ctx context.Context, parentID string) (int, error) {
	var count int
	err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM nodes WHERE parent_id = ? AND deleted_at IS NULL`,
		parentID,
	).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count children of %s: %w", parentID, err)
	}
	return count, nil
}
