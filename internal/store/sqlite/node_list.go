// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// ListNodes returns nodes matching the filter with pagination per FR-6.3.
// The third return value is the total count matching the filter (ignoring pagination).
// All filtering uses parameterized queries — no string concatenation.
// Excludes soft-deleted nodes (deleted_at IS NULL).
func (s *Store) ListNodes(ctx context.Context, filter store.NodeFilter, opts store.ListOptions) ([]*model.Node, int, error) {
	// Build WHERE clauses and args.
	clauses, args := buildFilterClauses(filter)

	// Always exclude soft-deleted.
	clauses = append(clauses, "deleted_at IS NULL")

	whereSQL := "WHERE " + strings.Join(clauses, " AND ")

	// Count total matching rows.
	countQuery := fmt.Sprintf("SELECT COUNT(*) FROM nodes %s", whereSQL)
	var total int
	if err := s.readDB.QueryRowContext(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count nodes: %w", err)
	}

	// If limit is 0, return count only (used by stats).
	if opts.Limit == 0 {
		return nil, total, nil
	}

	// Fetch paginated results.
	selectQuery := fmt.Sprintf(
		"SELECT %s FROM nodes %s ORDER BY priority ASC, created_at ASC LIMIT ? OFFSET ?",
		nodeColumns, whereSQL,
	)
	queryArgs := make([]any, 0, len(args)+2)
	queryArgs = append(queryArgs, args...)
	queryArgs = append(queryArgs, opts.Limit, opts.Offset)

	rows, err := s.readDB.QueryContext(ctx, selectQuery, queryArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("list nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close list rows", "error", closeErr)
		}
	}()

	var nodes []*model.Node
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan list node: %w", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate list nodes: %w", err)
	}

	return nodes, total, nil
}

// buildFilterClauses builds SQL WHERE clauses from a NodeFilter.
// Returns clauses and parameterized args — never concatenates user input into SQL.
func buildFilterClauses(filter store.NodeFilter) ([]string, []any) {
	var clauses []string
	var args []any

	// Status filter: IN clause with parameterized placeholders.
	if len(filter.Status) > 0 {
		placeholders := make([]string, len(filter.Status))
		for i, s := range filter.Status {
			placeholders[i] = "?"
			args = append(args, string(s))
		}
		clauses = append(clauses,
			fmt.Sprintf("status IN (%s)", strings.Join(placeholders, ",")))
	}

	// Under filter: subtree using LIKE with parameterized prefix.
	// Matches the parent itself OR any descendant (id LIKE 'parent.%').
	if filter.Under != "" {
		clauses = append(clauses, "(id = ? OR id LIKE ? ESCAPE '\\')")
		args = append(args, filter.Under, filter.Under+".%")
	}

	// Assignee filter.
	if filter.Assignee != "" {
		clauses = append(clauses, "assignee = ?")
		args = append(args, filter.Assignee)
	}

	// NodeType filter.
	if filter.NodeType != "" {
		clauses = append(clauses, "node_type = ?")
		args = append(args, filter.NodeType)
	}

	// Priority filter.
	if filter.Priority != nil {
		clauses = append(clauses, "priority = ?")
		args = append(args, *filter.Priority)
	}

	// Labels filter: JSON array contains check.
	// Uses json_each to search within the JSON labels array.
	for _, label := range filter.Labels {
		clauses = append(clauses,
			"EXISTS (SELECT 1 FROM json_each(labels) WHERE json_each.value = ?)")
		args = append(args, label)
	}

	return clauses, args
}
