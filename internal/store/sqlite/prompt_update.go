// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
)

// UpdatePrompt updates a prompt (FR-12.1) and returns the stored content hashes
// immediately before and after its write (FR-3.7), inside the history transaction.
// Results are successful only after commit; every error returns empty hashes.
func (s *Store) UpdatePrompt(ctx context.Context, id, text string) (string, string, error) {
	var oldHash, newHash string
	err := s.withHistoryTx(ctx, historyScope{id: id}, func(tx *sql.Tx) error {
		prior, readErr := promptContentHash(ctx, tx, id)
		if readErr != nil {
			return readErr
		}
		oldHash = prior
		if updateErr := s.executeUpdateTx(ctx, tx, id, &store.NodeUpdate{Prompt: &text}); updateErr != nil {
			return updateErr
		}
		next, readErr := promptContentHash(ctx, tx, id)
		if readErr != nil {
			return readErr
		}
		newHash = next
		return nil
	})
	if err != nil {
		return "", "", fmt.Errorf("update prompt for %s: %w", id, err)
	}
	return oldHash, newHash, nil
}

func promptContentHash(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	var hash string
	err := tx.QueryRowContext(ctx,
		`SELECT content_hash FROM nodes WHERE id = ? AND deleted_at IS NULL`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		err = model.ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read prompt content hash for %s: %w", id, err)
	}
	return hash, nil
}
