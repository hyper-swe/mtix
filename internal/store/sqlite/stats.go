// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store"
)

// GetStats returns aggregate statistics for a given scope per FR-2.7.5.
// If scopeID is empty, returns global statistics across all nodes.
// If scopeID is specified, scopes to that subtree (node + descendants).
// Excludes soft-deleted nodes from all counts.
func (s *Store) GetStats(
	ctx context.Context,
	scopeID string,
) (*store.Stats, error) {
	stats := &store.Stats{
		ByStatus:   make(map[string]int),
		ByPriority: make(map[string]int),
		ByType:     make(map[string]int),
		ScopeID:    scopeID,
	}

	// Build scope condition.
	scopeWhere, scopeArgs := buildScopeClause(scopeID)

	// Total count.
	countSQL := fmt.Sprintf(
		"SELECT COUNT(*) FROM nodes WHERE deleted_at IS NULL%s", scopeWhere)
	if err := s.readDB.QueryRowContext(ctx, countSQL, scopeArgs...).Scan(&stats.TotalNodes); err != nil {
		return nil, fmt.Errorf("count total nodes: %w", err)
	}

	if stats.TotalNodes == 0 {
		return stats, nil
	}

	// Counts by status.
	if err := s.aggregateBy(ctx, "status", scopeWhere, scopeArgs, stats.ByStatus); err != nil {
		return nil, fmt.Errorf("count by status: %w", err)
	}

	// Counts by priority.
	if err := s.aggregateBy(ctx, "priority", scopeWhere, scopeArgs, stats.ByPriority); err != nil {
		return nil, fmt.Errorf("count by priority: %w", err)
	}

	// Counts by node_type.
	if err := s.aggregateBy(ctx, "node_type", scopeWhere, scopeArgs, stats.ByType); err != nil {
		return nil, fmt.Errorf("count by type: %w", err)
	}

	// Overall progress: weighted average of progress values.
	progressSQL := fmt.Sprintf(
		`SELECT COALESCE(SUM(progress * weight) / NULLIF(SUM(weight), 0), 0.0)
		 FROM nodes WHERE deleted_at IS NULL%s`, scopeWhere)
	if err := s.readDB.QueryRowContext(ctx, progressSQL, scopeArgs...).Scan(&stats.Progress); err != nil {
		return nil, fmt.Errorf("calculate progress: %w", err)
	}

	return stats, nil
}

// aggregateBy runs a GROUP BY query for the given column and populates the result map.
// Uses parameterized queries exclusively.
func (s *Store) aggregateBy(
	ctx context.Context,
	column string,
	scopeWhere string,
	scopeArgs []any,
	result map[string]int,
) error {
	// column is an internal constant (status, priority, node_type) — not user input.
	query := fmt.Sprintf(
		"SELECT %s, COUNT(*) FROM nodes WHERE deleted_at IS NULL%s GROUP BY %s",
		column, scopeWhere, column)

	rows, err := s.readDB.QueryContext(ctx, query, scopeArgs...)
	if err != nil {
		return fmt.Errorf("aggregate by %s: %w", column, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close stats rows", "error", closeErr)
		}
	}()

	for rows.Next() {
		var key sql.NullString
		var count int
		if err := rows.Scan(&key, &count); err != nil {
			return fmt.Errorf("scan %s aggregate: %w", column, err)
		}
		if key.Valid {
			result[key.String] = count
		}
	}

	return rows.Err()
}

// buildScopeClause returns a WHERE clause fragment and args for scoping.
// If scopeID is empty, returns empty clause for global scope.
// Uses parameterized queries for the scope filter.
func buildScopeClause(scopeID string) (string, []any) {
	if scopeID == "" {
		return "", nil
	}
	return " AND (id = ? OR id LIKE ? ESCAPE '\\')", []any{scopeID, scopeID + ".%"}
}
