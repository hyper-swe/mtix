// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// GetAncestorChain returns the full ancestor chain from root to the given node (inclusive).
// The chain is returned in root-first order per FR-12.2.
// Walks parent_id links from the target node up to the root, then reverses.
func (s *Store) GetAncestorChain(ctx context.Context, nodeID string) ([]*model.Node, error) {
	// Start with the target node and walk up via parent_id.
	var chain []*model.Node
	currentID := nodeID

	for currentID != "" {
		node, err := s.GetNode(ctx, currentID)
		if err != nil {
			return nil, fmt.Errorf("get ancestor %s: %w", currentID, err)
		}
		chain = append(chain, node)
		currentID = node.ParentID
	}

	// Reverse to root-first order.
	reverseNodes(chain)
	return chain, nil
}

// GetSiblings returns direct children of the node's parent, excluding the node itself.
// For root nodes (no parent), returns empty slice.
// Excludes soft-deleted nodes.
func (s *Store) GetSiblings(ctx context.Context, nodeID string) ([]*model.Node, error) {
	// Get the node to find its parent.
	node, err := s.GetNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("get node %s for siblings: %w", nodeID, err)
	}

	if node.ParentID == "" {
		return nil, nil // Root nodes have no siblings.
	}

	// Query siblings: same parent, different ID.
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT `+nodeColumns+` FROM nodes
		 WHERE parent_id = ? AND id != ? AND deleted_at IS NULL
		 ORDER BY seq ASC`,
		node.ParentID, nodeID,
	)
	if err != nil {
		return nil, fmt.Errorf("query siblings of %s: %w", nodeID, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close rows", "error", closeErr)
		}
	}()

	var siblings []*model.Node
	for rows.Next() {
		sibling, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("scan sibling of %s: %w", nodeID, err)
		}
		siblings = append(siblings, sibling)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate siblings of %s: %w", nodeID, err)
	}

	return siblings, nil
}

// SetAnnotations replaces all annotations on a node per FR-3.4.
// The annotations are stored as a JSON array.
func (s *Store) SetAnnotations(ctx context.Context, nodeID string, annotations []model.Annotation) error {
	var annotJSON string
	if len(annotations) > 0 {
		data, err := json.Marshal(annotations)
		if err != nil {
			return fmt.Errorf("marshal annotations for %s: %w", nodeID, err)
		}
		annotJSON = string(data)
	}

	_, err := s.writeDB.ExecContext(ctx,
		`UPDATE nodes SET annotations = ? WHERE id = ? AND deleted_at IS NULL`,
		nullableString(annotJSON), nodeID,
	)
	if err != nil {
		return fmt.Errorf("set annotations for %s: %w", nodeID, err)
	}

	return nil
}

// reverseNodes reverses a slice of nodes in place.
func reverseNodes(nodes []*model.Node) {
	for i, j := 0, len(nodes)-1; i < j; i, j = i+1, j-1 {
		nodes[i], nodes[j] = nodes[j], nodes[i]
	}
}
