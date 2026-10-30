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
	err := tx.QueryRowContext(ctx, "SELECT author_id,payload FROM sync_events WHERE rowid=? AND op_type=?", key.EventSeq, model.OpComment).Scan(&author, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read addressed wake author: %w", err)
	}
	var p model.CommentPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return "", fmt.Errorf("decode addressed wake author: %w", err)
	}
	if p.To != key.AgentID {
		return "", nil
	}
	if p.AuthorID != "" {
		author = p.AuthorID
	}
	return author, nil
}
