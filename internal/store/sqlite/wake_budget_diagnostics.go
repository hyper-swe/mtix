// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeBudgetCounters reads local accounting without initializing any records.
func (s *Store) WakeBudgetCounters(ctx context.Context) (result []model.WakeBudgetCounter, err error) {
	source, err := s.WakeBudgetSource(ctx)
	if err != nil {
		return nil, fmt.Errorf("identify diagnostic wake source: %w", err)
	}
	boardID, err := s.WakeBudgetBoardID(ctx)
	if err != nil {
		return nil, fmt.Errorf("identify diagnostic wake board: %w", err)
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT agent_id,host_id,uid,total FROM wake_budget_counters
		ORDER BY agent_id,host_id,uid`)
	if err != nil {
		return nil, fmt.Errorf("read wake budget counters: %w", err)
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	result = []model.WakeBudgetCounter{}
	for rows.Next() {
		var row model.WakeBudgetCounter
		if err = rows.Scan(&row.Recipient.AgentID, &row.Recipient.HostID, &row.Recipient.UID, &row.Total); err != nil {
			return nil, fmt.Errorf("scan wake budget counter: %w", err)
		}
		row.Source, row.BoardID = source, boardID
		if row.Total < 0 || row.Source == "" {
			return nil, fmt.Errorf("wake budget counter metadata is invalid: %w", model.ErrInvalidInput)
		}
		result = append(result, row)
	}
	if err = rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate wake budget counters: %w", err)
	}
	return result, nil
}
