// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// validateIssueTypeEvent validates classification before dedupe or LWW per FR-3.1.
// Even a held, duplicate or losing event must not bypass payload validity checks.
func validateIssueTypeEvent(event *model.SyncEvent) error {
	if event.OpType != model.OpUpdateField {
		return nil
	}
	var payload model.UpdateFieldPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		return fmt.Errorf("decode update field payload: %w: %w", err, model.ErrInvalidInput)
	}
	if payload.FieldName != "issue_type" {
		return nil
	}
	_, err := decodeIssueTypeUpdate(payload.NewValue)
	return err
}

// decodeIssueTypeUpdate validates classification sync updates per FR-3.1.
// Empty strings clear the field; missing, null and non-string payloads are invalid.
func decodeIssueTypeUpdate(raw json.RawMessage) (any, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("decode issue type: %w: %w", err, model.ErrInvalidInput)
	}
	kind, ok := value.(string)
	if !ok {
		return nil, fmt.Errorf("issue type must be a string: %w", model.ErrInvalidInput)
	}
	if err := model.ValidateIssueType(model.IssueType(kind)); err != nil {
		return nil, err
	}
	return nullStr(kind), nil
}

// applyIssueTypeField persists a validated classification update per FR-3.1.
func applyIssueTypeField(ctx context.Context, tx *sql.Tx, id string, value any, now string) error {
	if _, err := tx.ExecContext(ctx, `UPDATE nodes SET issue_type = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`, value, now, id); err != nil {
		return fmt.Errorf("apply issue type to %s: %w", id, err)
	}
	return nil
}
