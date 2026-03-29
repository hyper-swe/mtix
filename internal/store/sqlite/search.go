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

// nodeColumnsQualified returns nodeColumns with each column prefixed by "n."
// for use in JOINs where column names might be ambiguous (e.g., FTS5 joins).
func nodeColumnsQualified() string {
	cols := strings.Split(nodeColumns, ",")
	qualified := make([]string, len(cols))
	for i, col := range cols {
		qualified[i] = "n." + strings.TrimSpace(col)
	}
	return strings.Join(qualified, ", ")
}

// SearchNodes performs full-text search via FTS5 per NFR-2.7.
// Uses MATCH query against nodes_fts (title, description, prompt),
// joins back to nodes, excludes soft-deleted, and ranks by relevance.
//
// Returns matching nodes, total count, and any error.
func (s *Store) SearchNodes(
	ctx context.Context,
	query string,
	filter store.NodeFilter,
	opts store.ListOptions,
) ([]*model.Node, int, error) {
	if query == "" {
		return nil, 0, fmt.Errorf("search query is required: %w", model.ErrInvalidInput)
	}

	// Build WHERE clauses.
	where := []string{"n.deleted_at IS NULL"}
	args := []any{}

	// Apply additional filters.
	if len(filter.Status) > 0 {
		placeholders := make([]string, len(filter.Status))
		for i, s := range filter.Status {
			placeholders[i] = "?"
			args = append(args, string(s))
		}
		where = append(where, fmt.Sprintf("n.status IN (%s)", strings.Join(placeholders, ",")))
	}
	if filter.Under != "" {
		where = append(where, "(n.id = ? OR n.id LIKE ?)")
		args = append(args, filter.Under, filter.Under+".%")
	}
	if filter.Assignee != "" {
		where = append(where, "n.assignee = ?")
		args = append(args, filter.Assignee)
	}

	whereClause := strings.Join(where, " AND ")

	// Count query. Use subquery for FTS5 MATCH (content-sync tables require
	// the table name in MATCH, not an alias).
	countSQL := fmt.Sprintf(
		`SELECT COUNT(*) FROM nodes n
		 WHERE n.rowid IN (SELECT rowid FROM nodes_fts WHERE nodes_fts MATCH ?)
		   AND %s`, whereClause)
	countArgs := append([]any{query}, args...)

	var total int
	if err := s.readDB.QueryRowContext(ctx, countSQL, countArgs...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count search results: %w", err)
	}

	if total == 0 || opts.Limit == 0 {
		return nil, total, nil
	}

	// Data query with ranking via subquery. FTS5 rank is accessed through
	// a JOIN, but we use the table name for MATCH and qualify all node columns.
	qualifiedCols := nodeColumnsQualified()
	dataSQL := fmt.Sprintf(
		`SELECT %s FROM nodes n
		 INNER JOIN nodes_fts ON n.rowid = nodes_fts.rowid
		 WHERE nodes_fts MATCH ? AND %s
		 ORDER BY nodes_fts.rank
		 LIMIT ? OFFSET ?`, qualifiedCols, whereClause)
	dataArgs := append([]any{query}, args...)
	dataArgs = append(dataArgs, opts.Limit, opts.Offset)

	rows, err := s.readDB.QueryContext(ctx, dataSQL, dataArgs...)
	if err != nil {
		return nil, 0, fmt.Errorf("search nodes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close rows", "error", closeErr)
		}
	}()

	var nodes []*model.Node
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, 0, fmt.Errorf("scan search result: %w", err)
		}
		nodes = append(nodes, node)
	}

	return nodes, total, rows.Err()
}
