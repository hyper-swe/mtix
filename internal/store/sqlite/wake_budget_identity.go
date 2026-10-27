// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/hyper-swe/mtix/internal/model"
)

const wakeBudgetBoardIDKey = "wake_budget_board_id"

// WakeBudgetBoardID reads the local-only board identity without initializing it.
func (s *Store) WakeBudgetBoardID(ctx context.Context) (string, error) {
	id, err := readWakeBudgetBoardID(ctx, s.readDB)
	if err != nil {
		return "", fmt.Errorf("read board identity for wake accounting: %w", err)
	}
	return id, nil
}

func readWakeBudgetBoardID(ctx context.Context, q wakeBudgetQuery) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", wakeBudgetBoardIDKey).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read local wake board identity: %w", err)
	}
	if err := validateWakeBudgetBoardID(id); err != nil {
		return "", fmt.Errorf("validate local wake board identity: %w", err)
	}
	return id, nil
}

func validateWakeBudgetBoardID(id string) error {
	parsed, err := uuid.Parse(id)
	if err != nil || parsed == uuid.Nil || parsed.String() != id {
		return fmt.Errorf("local wake board identity is invalid: %w", model.ErrInvalidInput)
	}
	return nil
}

func acceptWakeBudgetBoardID(ctx context.Context, tx *sql.Tx, p model.WakeBudgetProposal, chosen string) (string, error) {
	if chosen == "" {
		chosen = p.BoardCandidate
	}
	if p.Snapshot.BoardID != "" && chosen != p.Snapshot.BoardID {
		return "", fmt.Errorf("local wake board identity changed during admission: %w", model.ErrConflict)
	}
	if err := validateWakeBudgetBoardID(chosen); err != nil {
		return "", fmt.Errorf("validate local wake board identity: %w", err)
	}
	if p.Snapshot.BoardID == "" {
		if _, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES(?,?)", wakeBudgetBoardIDKey, chosen); err != nil {
			return "", fmt.Errorf("persist local wake board identity: %w", err)
		}
	}
	return chosen, nil
}
