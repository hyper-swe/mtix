// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/google/uuid"

	"github.com/hyper-swe/mtix/internal/model"
)

type wakeBudgetQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

// WakeAckMark counts acknowledged recipient inbox events without mutation.
// Direct and hook deliveries, and overlapping acknowledgements, count once.
func (s *Store) WakeAckMark(ctx context.Context, agent string) (int64, error) {
	mark, err := readWakeAckMark(ctx, s.readDB, agent)
	if err != nil {
		return 0, fmt.Errorf("read local wake acknowledgement mark: %w", err)
	}
	return mark, nil
}

func readWakeAckMark(ctx context.Context, q wakeBudgetQuery, agent string) (int64, error) {
	var cursor int64
	if err := q.QueryRowContext(ctx, `SELECT COALESCE(
  (SELECT cursor FROM agent_inbox_cursor WHERE agent_id=?),0)`, agent).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("read wake acknowledgement cursor: %w", err)
	}
	if cursor < 0 {
		return 0, fmt.Errorf("wake acknowledgement coverage is invalid: %w", model.ErrInvalidInput)
	}
	// Use the inbox eligibility set, rather than the global journal cursor:
	// advancing over foreign or already selectively acked events has no effect.
	var pending, total int64
	err := q.QueryRowContext(ctx, inboxCountSQL, agent, agent, agent, agent).Scan(&pending, &total)
	if err != nil {
		return 0, fmt.Errorf("read effective wake acknowledgement: %w", err)
	}
	return total - pending, nil
}

// WakeBudgetSource identifies this canonical database location without mutation.
// Keeping its key outside the database's contents makes a pre-budget restore
// recognizable, even when every accounting row has disappeared.
func (s *Store) WakeBudgetSource(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("identify wake counter source: %w", err)
	}
	var location string
	if err := s.readDB.QueryRowContext(ctx, "SELECT file FROM pragma_database_list WHERE name=?", "main").Scan(&location); err != nil {
		return "", fmt.Errorf("read connected wake counter source: %w", err)
	}
	if location == "" {
		return "", fmt.Errorf("wake accounting requires a persistent database: %w", model.ErrInvalidInput)
	}
	canonical, err := filepath.EvalSymlinks(location)
	if err != nil {
		return "", fmt.Errorf("resolve wake counter database: %w", err)
	}
	return uuid.NewHash(sha256.New(), uuid.NameSpaceURL, []byte(canonical), 8).String(), nil
}

func readWakeBudgetTotal(ctx context.Context, tx *sql.Tx, key model.WakeRecipient) (int64, error) {
	var total int64
	err := tx.QueryRowContext(ctx, "SELECT total FROM wake_budget_counters WHERE agent_id=? AND host_id=? AND uid=?",
		key.AgentID, key.HostID, key.UID).Scan(&total)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read wake counter: %w", err)
	}
	if total < 0 {
		return 0, fmt.Errorf("negative wake counter: %w", model.ErrInvalidInput)
	}
	return total, nil
}

func (s *Store) readWakeBudgetSnapshot(ctx context.Context, tx *sql.Tx, key model.WakeRecipient) (model.WakeBudgetSnapshot, error) {
	var snap model.WakeBudgetSnapshot
	var err error
	if snap.Source, err = s.WakeBudgetSource(ctx); err != nil {
		return snap, fmt.Errorf("read local wake accounting snapshot: %w", err)
	}
	if snap.Total, err = readWakeBudgetTotal(ctx, tx, key); err != nil {
		return snap, fmt.Errorf("read local wake accounting snapshot: %w", err)
	}
	if snap.BoardID, err = readWakeBudgetBoardID(ctx, tx); err != nil {
		return snap, fmt.Errorf("read wake snapshot identity: %w", err)
	}
	snap.AckMark, err = readWakeAckMark(ctx, tx, key.AgentID)
	if err != nil {
		return snap, fmt.Errorf("read snapshot acknowledgement coverage: %w", err)
	}
	return snap, nil
}
