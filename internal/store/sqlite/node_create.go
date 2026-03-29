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

// CreateNode persists a new node per FR-2.7.
// The node's ID, defaults, and content_hash must be pre-set by the caller.
// Validates parent status (FR-3.9), inserts the node, appends a 'created'
// activity entry, and recalculates parent progress in the same transaction (FR-5.7).
//
// Returns ErrAlreadyExists if a node with the given ID already exists.
// Returns ErrInvalidInput if the parent has a terminal status.
func (s *Store) CreateNode(ctx context.Context, node *model.Node) error {
	if err := node.Validate(); err != nil {
		return fmt.Errorf("create node validate: %w", err)
	}

	return s.WithTx(ctx, func(tx *sql.Tx) error {
		if node.ParentID != "" {
			if err := validateParentStatus(ctx, tx, node.ParentID); err != nil {
				return err
			}
		}

		if err := insertNode(ctx, tx, node); err != nil {
			return err
		}

		if node.ParentID != "" {
			if err := recalculateProgress(ctx, tx, node.ParentID); err != nil {
				return fmt.Errorf("recalculate progress after create: %w", err)
			}
		}

		return nil
	})
}

// nodeJSONFields holds the serialized JSON fields for a node insert.
type nodeJSONFields struct {
	labels      sql.NullString
	codeRefs    sql.NullString
	commitRefs  sql.NullString
	annotations sql.NullString
	activity    string
	metadata    sql.NullString
}

// marshalNodeFields serializes a node's JSON fields for insertion.
func marshalNodeFields(node *model.Node) (*nodeJSONFields, error) {
	labelsJSON, err := marshalJSONField(node.Labels)
	if err != nil {
		return nil, fmt.Errorf("marshal labels: %w", err)
	}
	codeRefsJSON, err := marshalJSONField(node.CodeRefs)
	if err != nil {
		return nil, fmt.Errorf("marshal code_refs: %w", err)
	}
	commitRefsJSON, err := marshalJSONField(node.CommitRefs)
	if err != nil {
		return nil, fmt.Errorf("marshal commit_refs: %w", err)
	}
	annotationsJSON, err := marshalJSONField(node.Annotations)
	if err != nil {
		return nil, fmt.Errorf("marshal annotations: %w", err)
	}
	activityJSON, err := buildCreatedActivity(node.Creator, node.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("build activity: %w", err)
	}

	var metadataStr sql.NullString
	if node.Metadata != nil {
		metadataStr = sql.NullString{String: string(node.Metadata), Valid: true}
	}

	return &nodeJSONFields{
		labels:      labelsJSON,
		codeRefs:    codeRefsJSON,
		commitRefs:  commitRefsJSON,
		annotations: annotationsJSON,
		activity:    activityJSON,
		metadata:    metadataStr,
	}, nil
}

// insertNode inserts a node into the database with all fields.
func insertNode(ctx context.Context, tx *sql.Tx, node *model.Node) error {
	fields, err := marshalNodeFields(node)
	if err != nil {
		return err
	}

	// INSERT — parameterized query per SQL Rule #1.
	_, err = tx.ExecContext(ctx, `
		INSERT INTO nodes (
			id, parent_id, depth, seq, project,
			title, description, prompt, acceptance,
			node_type, issue_type, priority, labels,
			status, previous_status, progress, assignee, creator, agent_state,
			created_at, updated_at, closed_at, defer_until,
			estimate_min, actual_min, weight, content_hash,
			code_refs, commit_refs,
			annotations, invalidated_at, invalidated_by, invalidation_reason,
			activity,
			deleted_at, deleted_by,
			metadata, session_id
		) VALUES (
			?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?, ?, ?,
			?, ?,
			?, ?, ?, ?,
			?,
			?, ?,
			?, ?
		)`,
		node.ID, nullableString(node.ParentID), node.Depth, node.Seq, node.Project,
		node.Title, nullableString(node.Description), nullableString(node.Prompt), nullableString(node.Acceptance),
		string(node.NodeType), nullableString(string(node.IssueType)), node.Priority, fields.labels,
		string(node.Status), nullableString(string(node.PreviousStatus)), node.Progress,
		nullableString(node.Assignee), nullableString(node.Creator), nullableString(string(node.AgentState)),
		node.CreatedAt.UTC().Format(time.RFC3339), node.UpdatedAt.UTC().Format(time.RFC3339),
		nullableTime(node.ClosedAt), nullableTime(node.DeferUntil),
		nullableInt(node.EstimateMin), nullableInt(node.ActualMin), node.Weight, nullableString(node.ContentHash),
		fields.codeRefs, fields.commitRefs,
		fields.annotations, nullableTime(node.InvalidatedAt),
		nullableString(node.InvalidatedBy), nullableString(node.InvalidationReason),
		fields.activity,
		nullableTime(node.DeletedAt), nullableString(node.DeletedBy),
		fields.metadata, nullableString(node.SessionID),
	)
	if err != nil {
		if isUniqueConstraintError(err) {
			return fmt.Errorf("node %s already exists: %w", node.ID, model.ErrAlreadyExists)
		}
		return fmt.Errorf("insert node %s: %w", node.ID, err)
	}

	return nil
}

// validateParentStatus checks that the parent node is not in a terminal state per FR-3.9.
func validateParentStatus(ctx context.Context, tx *sql.Tx, parentID string) error {
	var status string
	err := tx.QueryRowContext(ctx,
		`SELECT status FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		parentID,
	).Scan(&status)
	if err == sql.ErrNoRows {
		return fmt.Errorf("parent %s not found: %w", parentID, model.ErrNotFound)
	}
	if err != nil {
		return fmt.Errorf("check parent %s status: %w", parentID, err)
	}

	if model.Status(status).IsTerminal() {
		return fmt.Errorf(
			"cannot create child under %s parent %s; reopen it first: %w",
			status, parentID, model.ErrInvalidInput,
		)
	}

	return nil
}

// buildCreatedActivity creates the initial activity entry for a new node.
func buildCreatedActivity(creator string, createdAt time.Time) (string, error) {
	entry := model.ActivityEntry{
		ID:        fmt.Sprintf("act-%d", createdAt.UnixNano()),
		Type:      model.ActivityTypeCreated,
		Author:    creator,
		Text:      "Node created",
		CreatedAt: createdAt,
	}

	data, err := json.Marshal([]model.ActivityEntry{entry})
	if err != nil {
		return "", fmt.Errorf("marshal activity: %w", err)
	}

	return string(data), nil
}

// isUniqueConstraintError checks if the error is a UNIQUE constraint violation.
func isUniqueConstraintError(err error) bool {
	if err == nil {
		return false
	}
	// modernc.org/sqlite returns errors containing "UNIQUE constraint failed".
	return containsString(err.Error(), "UNIQUE constraint failed")
}

// containsString checks if s contains substr.
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

// searchString is a simple substring search.
func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
