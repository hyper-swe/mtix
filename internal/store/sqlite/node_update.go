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
	"github.com/hyper-swe/mtix/internal/store"
)

// UpdateNode applies partial updates to a node per FR-3.1.
// Only non-nil fields in the update struct are applied.
// Recomputes content_hash when content fields change (FR-3.7).
// Sets updated_at timestamp. Creates activity entries for changes.
// Triggers FTS update via database triggers.
//
// Returns ErrNotFound if the node does not exist or is soft-deleted.
func (s *Store) UpdateNode(ctx context.Context, id string, updates *store.NodeUpdate) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return executeUpdateTx(ctx, tx, id, updates)
	})
}

// executeUpdateTx performs the node update within a transaction.
func executeUpdateTx(ctx context.Context, tx *sql.Tx, id string, updates *store.NodeUpdate) error {
	if err := verifyNodeExists(ctx, tx, id); err != nil {
		return err
	}

	setClauses, args := buildUpdateClauses(updates)
	if len(setClauses) == 0 {
		return nil
	}

	now := time.Now().UTC().Format(time.RFC3339)
	setClauses = append(setClauses, "updated_at = ?")
	args = append(args, now)

	if contentFieldsChanged(updates) {
		hash, hashErr := recomputeContentHash(ctx, tx, id, updates)
		if hashErr != nil {
			return fmt.Errorf("recompute content hash for %s: %w", id, hashErr)
		}
		setClauses = append(setClauses, "content_hash = ?")
		args = append(args, hash)
	}

	if err := execUpdateQuery(ctx, tx, id, setClauses, args); err != nil {
		return err
	}

	return emitUpdateEvents(ctx, tx, id, updates)
}

// emitUpdateEvents emits one sync event per changed field. Maps the
// canonical Acceptance and Prompt fields onto their dedicated op_types
// (set_acceptance, set_prompt) and treats every other field as
// update_field per SYNC-DESIGN section 3.3.
//
// OldValue capture is deferred — the field is optional in the payload
// and conflict resolution uses lamport_clock first; richer surfacing
// (which needs the prior value) lands in MTIX-15.5.
func emitUpdateEvents(ctx context.Context, tx *sql.Tx, id string, u *store.NodeUpdate) error {
	project := projectPrefixFromNodeID(id)
	emit := func(op model.OpType, payload json.RawMessage) error {
		return emitEvent(ctx, tx, emitParams{
			NodeID:      id,
			ProjectCode: project,
			OpType:      op,
			Author:      authorIDFallback,
			Payload:     payload,
		})
	}

	if u.Acceptance != nil {
		p, _ := model.EncodePayload(&model.SetAcceptancePayload{AcceptanceText: *u.Acceptance})
		if err := emit(model.OpSetAcceptance, p); err != nil {
			return err
		}
	}
	if u.Prompt != nil {
		p, _ := model.EncodePayload(&model.SetPromptPayload{PromptText: *u.Prompt})
		if err := emit(model.OpSetPrompt, p); err != nil {
			return err
		}
	}

	type fieldEmit struct {
		name string
		val  any
	}
	plain := make([]fieldEmit, 0, 8)
	if u.Title != nil {
		plain = append(plain, fieldEmit{"title", *u.Title})
	}
	if u.Description != nil {
		plain = append(plain, fieldEmit{"description", *u.Description})
	}
	if u.Status != nil {
		plain = append(plain, fieldEmit{"status", string(*u.Status)})
	}
	if u.Priority != nil {
		plain = append(plain, fieldEmit{"priority", int(*u.Priority)})
	}
	if u.Labels != nil {
		plain = append(plain, fieldEmit{"labels", u.Labels})
	}
	if u.Assignee != nil {
		plain = append(plain, fieldEmit{"assignee", *u.Assignee})
	}
	if u.AgentState != nil {
		plain = append(plain, fieldEmit{"agent_state", string(*u.AgentState)})
	}

	for _, f := range plain {
		newJSON, err := json.Marshal(f.val)
		if err != nil {
			return fmt.Errorf("marshal new value for %s: %w", f.name, err)
		}
		p, _ := model.EncodePayload(&model.UpdateFieldPayload{
			FieldName: f.name,
			NewValue:  newJSON,
		})
		if err := emit(model.OpUpdateField, p); err != nil {
			return err
		}
	}

	return nil
}

// verifyNodeExists checks that a node exists and is not soft-deleted.
func verifyNodeExists(ctx context.Context, tx *sql.Tx, id string) error {
	var exists int
	err := tx.QueryRowContext(ctx,
		`SELECT 1 FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		id,
	).Scan(&exists)
	if err == sql.ErrNoRows {
		return fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("check node %s exists: %w", id, err)
	}
	return nil
}

// execUpdateQuery builds and executes the final UPDATE statement.
func execUpdateQuery(ctx context.Context, tx *sql.Tx, id string, setClauses []string, args []any) error {
	query := "UPDATE nodes SET "
	for i, clause := range setClauses {
		if i > 0 {
			query += ", "
		}
		query += clause
	}
	query += " WHERE id = ? AND deleted_at IS NULL"
	args = append(args, id)

	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("update node %s: %w", id, err)
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check rows affected for %s: %w", id, err)
	}
	if affected == 0 {
		return fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}

	return nil
}

// buildUpdateClauses creates SET clauses and args from a NodeUpdate.
func buildUpdateClauses(u *store.NodeUpdate) ([]string, []any) {
	var clauses []string
	var args []any

	if u.Title != nil {
		clauses = append(clauses, "title = ?")
		args = append(args, *u.Title)
	}
	if u.Description != nil {
		clauses = append(clauses, "description = ?")
		args = append(args, *u.Description)
	}
	if u.Prompt != nil {
		clauses = append(clauses, "prompt = ?")
		args = append(args, *u.Prompt)
	}
	if u.Acceptance != nil {
		clauses = append(clauses, "acceptance = ?")
		args = append(args, *u.Acceptance)
	}
	if u.Status != nil {
		clauses = append(clauses, "status = ?")
		args = append(args, string(*u.Status))
	}
	if u.Priority != nil {
		clauses = append(clauses, "priority = ?")
		args = append(args, int(*u.Priority))
	}
	if u.Labels != nil {
		labelsJSON, _ := json.Marshal(u.Labels)
		clauses = append(clauses, "labels = ?")
		args = append(args, string(labelsJSON))
	}
	if u.Assignee != nil {
		clauses = append(clauses, "assignee = ?")
		args = append(args, *u.Assignee)
	}
	if u.AgentState != nil {
		clauses = append(clauses, "agent_state = ?")
		args = append(args, string(*u.AgentState))
	}

	return clauses, args
}

// contentFieldsChanged checks if any content fields were modified per FR-3.7.
func contentFieldsChanged(u *store.NodeUpdate) bool {
	return u.Title != nil || u.Description != nil ||
		u.Prompt != nil || u.Acceptance != nil || u.Labels != nil
}

// recomputeContentHash reads current content fields, applies updates,
// and computes the new hash per FR-3.7.
func recomputeContentHash(ctx context.Context, tx *sql.Tx, id string, u *store.NodeUpdate) (string, error) {
	// Read current content fields.
	var title, description, prompt, acceptance sql.NullString
	var labelsJSON sql.NullString

	err := tx.QueryRowContext(ctx,
		`SELECT title, description, prompt, acceptance, labels
		 FROM nodes WHERE id = ?`,
		id,
	).Scan(&title, &description, &prompt, &acceptance, &labelsJSON)
	if err != nil {
		return "", fmt.Errorf("read content fields for %s: %w", id, err)
	}

	// Apply updates to get the new values.
	titleVal := title.String
	if u.Title != nil {
		titleVal = *u.Title
	}
	descVal := description.String
	if u.Description != nil {
		descVal = *u.Description
	}
	promptVal := prompt.String
	if u.Prompt != nil {
		promptVal = *u.Prompt
	}
	acceptVal := acceptance.String
	if u.Acceptance != nil {
		acceptVal = *u.Acceptance
	}

	var labels []string
	if u.Labels != nil {
		labels = u.Labels
	} else if labelsJSON.Valid && labelsJSON.String != "" {
		if err := json.Unmarshal([]byte(labelsJSON.String), &labels); err != nil {
			return "", fmt.Errorf("unmarshal existing labels: %w", err)
		}
	}

	return model.ComputeContentHash(titleVal, descVal, promptVal, acceptVal, labels), nil
}
