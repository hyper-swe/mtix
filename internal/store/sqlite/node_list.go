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

// buildFilterClauses builds SQL WHERE clauses from a NodeFilter per FR-17.1.
// Convenience wrapper around buildFilterClausesWithPrefix using the empty
// prefix (unaliased nodes table).
func buildFilterClauses(filter store.NodeFilter) ([]string, []any) {
	return buildFilterClausesWithPrefix(filter, "")
}

// buildFilterClausesWithPrefix builds SQL WHERE clauses from a NodeFilter,
// prefixing every column reference with `prefix` (e.g., "n." for joins).
// Returns clauses and parameterized args — NEVER concatenates user input
// into SQL. Every filter value goes through a "?" placeholder bound at
// query time. Multi-value fields use IN(?,?,?) or OR'd predicates per
// FR-17.1.
//
// Security note (FR-17 audit T9): the Under filter uses LIKE ? || '%'
// where the bound parameter is a literal node ID prefix. This is safe
// against LIKE wildcard injection ONLY because FR-2.1a constrains project
// prefixes to uppercase alphanumeric and hyphens, which excludes the
// SQLite LIKE wildcards `%` and `_`. If FR-2.1a ever loosens, this code
// MUST switch to ESCAPE-based wildcard escaping.
func buildFilterClausesWithPrefix(filter store.NodeFilter, prefix string) ([]string, []any) {
	var clauses []string
	var args []any

	addInClause := func(col string, count int, valueAdder func(int) any) {
		if count == 0 {
			return
		}
		placeholders := make([]string, count)
		for i := 0; i < count; i++ {
			placeholders[i] = "?"
			args = append(args, valueAdder(i))
		}
		clauses = append(clauses,
			fmt.Sprintf("%s%s IN (%s)", prefix, col, strings.Join(placeholders, ",")))
	}

	// Status filter: IN clause with parameterized placeholders.
	addInClause("status", len(filter.Status), func(i int) any { return string(filter.Status[i]) })

	// Under filter: per-value (id = ? OR id LIKE ? ESCAPE '\') joined by OR.
	// Each value contributes two bound parameters (exact match + descendant
	// prefix match). The whole group is parenthesized so AND-combination
	// with other clauses works correctly.
	if len(filter.Under) > 0 {
		predicates := make([]string, len(filter.Under))
		for i, u := range filter.Under {
			predicates[i] = fmt.Sprintf("(%sid = ? OR %sid LIKE ? ESCAPE '\\')", prefix, prefix)
			args = append(args, u, u+".%")
		}
		clauses = append(clauses, "("+strings.Join(predicates, " OR ")+")")
	}

	// Assignee, NodeType, Priority filters: IN clauses.
	addInClause("assignee", len(filter.Assignee), func(i int) any { return filter.Assignee[i] })
	addInClause("node_type", len(filter.NodeType), func(i int) any { return filter.NodeType[i] })
	addInClause("priority", len(filter.Priority), func(i int) any { return filter.Priority[i] })

	// Labels filter: JSON array contains check.
	// Uses json_each to search within the JSON labels array.
	for _, label := range filter.Labels {
		clauses = append(clauses,
			fmt.Sprintf("EXISTS (SELECT 1 FROM json_each(%slabels) WHERE json_each.value = ?)", prefix))
		args = append(args, label)
	}

	return clauses, args
}
