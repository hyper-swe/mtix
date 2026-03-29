// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// nodeColumns lists all columns in the nodes table for SELECT statements.
// ORDER MUST match the scanNode function's Scan parameters.
const nodeColumns = `id, parent_id, depth, seq, project,
    title, description, prompt, acceptance,
    node_type, issue_type, priority, labels,
    status, previous_status, progress, assignee, creator, agent_state,
    created_at, updated_at, closed_at, defer_until,
    estimate_min, actual_min, weight, content_hash,
    code_refs, commit_refs,
    annotations, invalidated_at, invalidated_by, invalidation_reason,
    activity,
    deleted_at, deleted_by,
    metadata, session_id`

// scanDest holds the intermediate scan destinations for a node row.
type scanDest struct {
	parentID, description, prompt, acceptance sql.NullString
	issueType, previousStatus, assignee      sql.NullString
	creator, agentState                       sql.NullString
	closedAt, deferUntil                      sql.NullString
	contentHash                               sql.NullString
	invalidatedAt, invalidatedBy              sql.NullString
	invalidationReason                        sql.NullString
	deletedAt, deletedBy                      sql.NullString
	sessionID                                 sql.NullString
	labelsJSON, codeRefsJSON, commitRefsJSON  sql.NullString
	annotationsJSON, activityJSON             sql.NullString
	metadataJSON                              sql.NullString
	estimateMin, actualMin                    sql.NullInt64
	createdAtStr, updatedAtStr                string
	nodeTypeStr, statusStr                    string
}

// scanNode scans a SQL row into a Node struct.
// Column order MUST match nodeColumns.
func scanNode(scanner interface{ Scan(dest ...any) error }) (*model.Node, error) {
	var n model.Node
	var d scanDest

	err := scanner.Scan(
		&n.ID, &d.parentID, &n.Depth, &n.Seq, &n.Project,
		&n.Title, &d.description, &d.prompt, &d.acceptance,
		&d.nodeTypeStr, &d.issueType, &n.Priority, &d.labelsJSON,
		&d.statusStr, &d.previousStatus, &n.Progress, &d.assignee, &d.creator, &d.agentState,
		&d.createdAtStr, &d.updatedAtStr, &d.closedAt, &d.deferUntil,
		&d.estimateMin, &d.actualMin, &n.Weight, &d.contentHash,
		&d.codeRefsJSON, &d.commitRefsJSON,
		&d.annotationsJSON, &d.invalidatedAt, &d.invalidatedBy, &d.invalidationReason,
		&d.activityJSON,
		&d.deletedAt, &d.deletedBy,
		&d.metadataJSON, &d.sessionID,
	)
	if err == sql.ErrNoRows {
		return nil, err
	}
	if err != nil {
		return nil, fmt.Errorf("scan node: %w", err)
	}

	assignScannedStrings(&n, &d)

	if err := parseScannedTimestamps(&n, &d); err != nil {
		return nil, err
	}

	assignScannedInts(&n, &d)

	if err := parseScannedJSON(&n, &d); err != nil {
		return nil, err
	}

	return &n, nil
}

// assignScannedStrings maps nullable scan destinations to node fields.
func assignScannedStrings(n *model.Node, d *scanDest) {
	n.ParentID = d.parentID.String
	n.Description = d.description.String
	n.Prompt = d.prompt.String
	n.Acceptance = d.acceptance.String
	n.NodeType = model.NodeType(d.nodeTypeStr)
	n.IssueType = model.IssueType(d.issueType.String)
	n.Status = model.Status(d.statusStr)
	n.PreviousStatus = model.Status(d.previousStatus.String)
	n.Assignee = d.assignee.String
	n.Creator = d.creator.String
	n.AgentState = model.AgentState(d.agentState.String)
	n.ContentHash = d.contentHash.String
	n.InvalidatedBy = d.invalidatedBy.String
	n.InvalidationReason = d.invalidationReason.String
	n.DeletedBy = d.deletedBy.String
	n.SessionID = d.sessionID.String
}

// parseScannedTimestamps parses timestamp strings from scan destinations.
func parseScannedTimestamps(n *model.Node, d *scanDest) error {
	var err error
	n.CreatedAt, err = time.Parse(time.RFC3339, d.createdAtStr)
	if err != nil {
		return fmt.Errorf("parse created_at %q: %w", d.createdAtStr, err)
	}

	n.UpdatedAt, err = time.Parse(time.RFC3339, d.updatedAtStr)
	if err != nil {
		return fmt.Errorf("parse updated_at %q: %w", d.updatedAtStr, err)
	}

	if err := parseNullableTime(d.closedAt, &n.ClosedAt); err != nil {
		return fmt.Errorf("parse closed_at: %w", err)
	}
	if err := parseNullableTime(d.deferUntil, &n.DeferUntil); err != nil {
		return fmt.Errorf("parse defer_until: %w", err)
	}
	if err := parseNullableTime(d.invalidatedAt, &n.InvalidatedAt); err != nil {
		return fmt.Errorf("parse invalidated_at: %w", err)
	}
	if err := parseNullableTime(d.deletedAt, &n.DeletedAt); err != nil {
		return fmt.Errorf("parse deleted_at: %w", err)
	}

	return nil
}

// assignScannedInts maps nullable integer scan destinations to node fields.
func assignScannedInts(n *model.Node, d *scanDest) {
	if d.estimateMin.Valid {
		v := int(d.estimateMin.Int64)
		n.EstimateMin = &v
	}
	if d.actualMin.Valid {
		v := int(d.actualMin.Int64)
		n.ActualMin = &v
	}
}

// parseScannedJSON unmarshals JSON fields from scan destinations.
func parseScannedJSON(n *model.Node, d *scanDest) error {
	if err := unmarshalJSONField(d.labelsJSON, &n.Labels); err != nil {
		return fmt.Errorf("parse labels: %w", err)
	}
	if err := unmarshalJSONField(d.codeRefsJSON, &n.CodeRefs); err != nil {
		return fmt.Errorf("parse code_refs: %w", err)
	}
	if err := unmarshalJSONField(d.commitRefsJSON, &n.CommitRefs); err != nil {
		return fmt.Errorf("parse commit_refs: %w", err)
	}
	if err := unmarshalJSONField(d.annotationsJSON, &n.Annotations); err != nil {
		return fmt.Errorf("parse annotations: %w", err)
	}
	if d.metadataJSON.Valid && d.metadataJSON.String != "" {
		n.Metadata = json.RawMessage(d.metadataJSON.String)
	}

	return nil
}

// parseNullableTime parses a nullable time string into a *time.Time.
func parseNullableTime(ns sql.NullString, dest **time.Time) error {
	if !ns.Valid || ns.String == "" {
		return nil
	}
	t, err := time.Parse(time.RFC3339, ns.String)
	if err != nil {
		return fmt.Errorf("parse time %q: %w", ns.String, err)
	}
	*dest = &t
	return nil
}

// unmarshalJSONField unmarshals a nullable JSON string into a destination.
func unmarshalJSONField[T any](ns sql.NullString, dest *T) error {
	if !ns.Valid || ns.String == "" || ns.String == "null" {
		return nil
	}
	if err := json.Unmarshal([]byte(ns.String), dest); err != nil {
		return fmt.Errorf("unmarshal JSON: %w", err)
	}
	return nil
}

// nullableString converts a string to sql.NullString.
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// nullableTime formats a *time.Time as a nullable ISO-8601 string.
func nullableTime(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: t.UTC().Format(time.RFC3339), Valid: true}
}

// nullableInt converts *int to sql.NullInt64.
func nullableInt(v *int) sql.NullInt64 {
	if v == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: int64(*v), Valid: true}
}

// marshalJSONField marshals a value to a JSON string, or returns NULL for nil/empty.
func marshalJSONField(v any) (sql.NullString, error) {
	if v == nil {
		return sql.NullString{}, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return sql.NullString{}, fmt.Errorf("marshal JSON: %w", err)
	}
	s := string(data)
	if s == "null" || s == "[]" {
		return sql.NullString{}, nil
	}
	return sql.NullString{String: s, Valid: true}, nil
}
