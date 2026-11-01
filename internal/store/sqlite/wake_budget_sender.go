// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// wakeBudgetSender uses the same payload-author precedence as InboxList.
// Legacy synthetic candidates have no event row and retain unknown attribution.
func wakeBudgetSender(ctx context.Context, tx *sql.Tx, key model.WakeKey) (string, error) {
	var author string
	var raw []byte
	// Read the journal row inside the claim transaction, including hook deliveries (FR-20.3).
	err := tx.QueryRowContext(ctx, "SELECT author_id,payload FROM sync_events WHERE rowid=?", key.EventSeq).Scan(&author, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read wake author: %w", err)
	}
	var p model.CommentPayload
	// InboxList falls back to the envelope when a hook payload is not a comment.
	if json.Unmarshal(raw, &p) == nil && p.AuthorID != "" {
		author = p.AuthorID
	}
	return author, nil
}
