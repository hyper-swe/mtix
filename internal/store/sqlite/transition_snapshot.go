// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// emitTransitionSnapshot captures the state actually written in this same
// transaction. Invalidation/reset can retain an earlier completion time;
// reconstructing it from the new event's clock would change that data.
func (s *Store) emitTransitionSnapshot(ctx context.Context, tx *sql.Tx, p emitParams, value *model.TransitionStatusPayload) error {
	raw, err := readClosedAtSnapshot(ctx, tx, p.NodeID)
	if err != nil {
		return err
	}
	value.ClosedAt = raw
	return s.emitPayload(ctx, tx, p, value)
}

func readClosedAtSnapshot(ctx context.Context, tx *sql.Tx, id string) (json.RawMessage, error) {
	var at sql.NullString
	if err := tx.QueryRowContext(ctx, `SELECT closed_at FROM nodes WHERE id = ?`, id).Scan(&at); err != nil {
		return nil, fmt.Errorf("read transition completion time: %w", err)
	}
	var stored any
	if at.Valid {
		stored = at.String
	}
	encoded, err := json.Marshal(stored)
	if err != nil {
		return nil, fmt.Errorf("encode transition completion time: %w", err)
	}
	return encoded, nil
}

// A repair of a metadata-bearing winner keeps the same authoritative value.
// Old winners retain the historical repair-event format and fallback rules.
func repairTransitionPayload(ctx context.Context, tx *sql.Tx, plan statusPlan) (json.RawMessage, error) {
	value := &model.TransitionStatusPayload{From: plan.row.status, To: plan.write.status, Reason: statusRepairReason}
	if plan.winner.payload.closedAt != nil {
		raw, err := readClosedAtSnapshot(ctx, tx, plan.row.id)
		if err != nil {
			return nil, fmt.Errorf("capture repair completion time: %w", err)
		}
		value.ClosedAt = raw
	}
	return model.EncodePayload(value)
}

// decodeClosedAtSnapshot keeps absence distinct from an explicit null. Older
// events retain the existing winner-table derivation and range fallback.
func decodeClosedAtSnapshot(raw json.RawMessage) (*workflowColumn, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var at *string
	if err := json.Unmarshal(raw, &at); err != nil {
		return nil, fmt.Errorf("decode closed_at: %w", err)
	}
	col := &workflowColumn{write: true}
	if at != nil {
		if _, err := time.Parse(time.RFC3339Nano, *at); err != nil {
			return nil, fmt.Errorf("validate closed_at: %w", err)
		}
		col.value = *at
	}
	return col, nil
}
