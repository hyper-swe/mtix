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

// WakeEventHop reads additive addressed-event chain context (FR-20.3).
// Missing legacy or synthetic event rows carry zero, as an absent payload field does.
// Malformed hop metadata warns and reads as zero, matching comment ingest.
func (s *Store) WakeEventHop(ctx context.Context, key model.WakeKey) (int64, error) {
	var raw []byte
	err := s.readDB.QueryRowContext(ctx, "SELECT payload FROM sync_events WHERE rowid=? AND op_type=?", key.EventSeq, model.OpComment).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read wake event hop: %w", err)
	}
	var payload model.CommentPayload
	if err = json.Unmarshal(raw, &payload); err != nil {
		return 0, fmt.Errorf("decode wake event hop: %w", err)
	}
	if payload.To == "" {
		return 0, nil
	}
	return payload.Hop, nil
}
