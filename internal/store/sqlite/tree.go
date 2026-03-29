// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// GetTree returns a node and all descendants up to maxDepth levels below it.
// Uses the materialized path (dot-notation ID) to find descendants via LIKE.
// If maxDepth is 0, returns only the root node itself.
// Excludes soft-deleted nodes. Returns ErrNotFound if root does not exist.
//
// Implements Store.GetTree per FR-2.7.4.
func (s *Store) GetTree(
	ctx context.Context,
	rootID string,
	maxDepth int,
) ([]*model.Node, error) {
	if rootID == "" {
		return nil, fmt.Errorf("root ID is required: %w", model.ErrInvalidInput)
	}

	// First verify root node exists and is not soft-deleted.
	root, err := s.GetNode(ctx, rootID)
	if err != nil {
		return nil, fmt.Errorf("get tree root: %w", err)
	}

	if maxDepth == 0 {
		return []*model.Node{root}, nil
	}

	// Calculate maximum allowed absolute depth.
	maxAbsDepth := root.Depth + maxDepth

	// Select root + descendants using LIKE on materialized path.
	// Filter by depth <= root.Depth + maxDepth and exclude soft-deleted.
	query := fmt.Sprintf(
		`SELECT %s FROM nodes
		 WHERE (id = ? OR id LIKE ? ESCAPE '\')
		   AND deleted_at IS NULL
		   AND depth <= ?
		 ORDER BY depth ASC, seq ASC`, nodeColumns)

	rows, err := s.readDB.QueryContext(ctx, query, rootID, rootID+".%", maxAbsDepth)
	if err != nil {
		return nil, fmt.Errorf("query tree: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close tree rows", "error", closeErr)
		}
	}()

	var nodes []*model.Node
	for rows.Next() {
		node, scanErr := scanNode(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan tree node: %w", scanErr)
		}
		nodes = append(nodes, node)
	}

	return nodes, rows.Err()
}
