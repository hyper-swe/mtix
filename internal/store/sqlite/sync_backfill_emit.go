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
	"github.com/hyper-swe/mtix/internal/sync/clock"
)

// emitBackfillForNode emits all events for one node: create + per-field
// update + optional transition + annotations. Accumulates counts into
// result.
func emitBackfillForNode(ctx context.Context, tx *sql.Tx, nr backfillNodeRow, result *BackfillResult) error {
	n := nr.node
	wallTS := n.CreatedAt.UnixMilli()

	createPayload, payloadErr := buildCreateNodePayload(n)
	if payloadErr != nil {
		return fmt.Errorf("build create_node payload for %s: %w", n.ID, payloadErr)
	}
	if emitErr := emitEvent(ctx, tx, emitParams{
		NodeID:      n.ID,
		ProjectCode: n.Project,
		OpType:      model.OpCreateNode,
		Author:      n.Creator,
		Payload:     createPayload,
		WallClockTS: wallTS,
	}); emitErr != nil {
		return fmt.Errorf("emit create_node for %s: %w", n.ID, emitErr)
	}
	result.CreateEvents++

	updateTS := n.UpdatedAt.UnixMilli()
	if fieldErr := emitBackfillFields(ctx, tx, n, updateTS, result); fieldErr != nil {
		return fieldErr
	}

	if backfillNeedsTransition(string(n.Status), n.ClosedAt != nil) {
		if transErr := emitBackfillTransition(ctx, tx, n, updateTS); transErr != nil {
			return transErr
		}
		result.TransitionEvents++
	}

	annCount, annErr := emitBackfillAnnotations(ctx, tx, n.ID, n.Project,
		n.Creator, nr.annotations, n.CreatedAt)
	if annErr != nil {
		return fmt.Errorf("emit annotations for %s: %w", n.ID, annErr)
	}
	result.AnnotateEvents += annCount
	return nil
}

// emitBackfillFields emits one update_field per non-default content
// field. Increments result.UpdateFieldEvents per emitted event.
func emitBackfillFields(ctx context.Context, tx *sql.Tx, n *model.Node, updateTS int64, result *BackfillResult) error {
	type fieldEntry struct {
		name, value string
	}
	for _, f := range []fieldEntry{
		{"description", n.Description},
		{"prompt", n.Prompt},
		{"acceptance", n.Acceptance},
		{"assignee", n.Assignee},
	} {
		if f.value == "" {
			continue
		}
		if err := emitBackfillUpdateField(ctx, tx, n.ID, n.Project, f.name, f.value, updateTS); err != nil {
			return err
		}
		result.UpdateFieldEvents++
	}
	return nil
}

// emitBackfillTransition emits one transition_status event for a node
// whose status is not the default 'open'.
func emitBackfillTransition(ctx context.Context, tx *sql.Tx, n *model.Node, updateTS int64) error {
	payload, payloadErr := model.EncodePayload(&model.TransitionStatusPayload{
		From: model.StatusOpen,
		To:   n.Status,
	})
	if payloadErr != nil {
		return fmt.Errorf("build transition payload for %s: %w", n.ID, payloadErr)
	}
	if emitErr := emitEvent(ctx, tx, emitParams{
		NodeID:      n.ID,
		ProjectCode: n.Project,
		OpType:      model.OpTransitionStatus,
		Author:      n.Creator,
		Payload:     payload,
		WallClockTS: updateTS,
	}); emitErr != nil {
		return fmt.Errorf("emit transition for %s: %w", n.ID, emitErr)
	}
	return nil
}

// emitBackfillDependencies walks the dependencies table and emits one
// link_dep event per row. Returns the emitted count.
func emitBackfillDependencies(ctx context.Context, tx *sql.Tx) (int, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT from_id, to_id, dep_type, created_at, created_by
		FROM dependencies
		ORDER BY created_at ASC, from_id ASC, to_id ASC`)
	if err != nil {
		return 0, fmt.Errorf("walk dependencies: %w", err)
	}
	defer func() { _ = rows.Close() }()

	count := 0
	for rows.Next() {
		var fromID, toID, depType, createdAt string
		var createdBy sql.NullString
		if scanErr := rows.Scan(&fromID, &toID, &depType, &createdAt, &createdBy); scanErr != nil {
			return count, fmt.Errorf("scan dep: %w", scanErr)
		}
		ts, _ := parseFlexibleTime(createdAt)
		author := ""
		if createdBy.Valid {
			author = createdBy.String
		}
		payload, payloadErr := model.EncodePayload(&model.LinkDepPayload{
			DependsOnNodeID: toID,
			DepType:         depType,
		})
		if payloadErr != nil {
			return count, fmt.Errorf("build link_dep payload: %w", payloadErr)
		}
		if emitErr := emitEvent(ctx, tx, emitParams{
			NodeID:      fromID,
			OpType:      model.OpLinkDep,
			Author:      author,
			Payload:     payload,
			WallClockTS: ts.UnixMilli(),
		}); emitErr != nil {
			return count, fmt.Errorf("emit link_dep %s->%s: %w", fromID, toID, emitErr)
		}
		count++
	}
	if iterErr := rows.Err(); iterErr != nil {
		return count, fmt.Errorf("walk dependencies: %w", iterErr)
	}
	return count, nil
}

// emitBackfillUpdateField is a thin helper to keep the main loop legible.
func emitBackfillUpdateField(ctx context.Context, tx *sql.Tx, nodeID, project, field, value string, wallTS int64) error {
	jsonVal, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshal value for %s.%s: %w", nodeID, field, err)
	}
	payload, err := model.EncodePayload(&model.UpdateFieldPayload{
		FieldName: field,
		NewValue:  jsonVal,
	})
	if err != nil {
		return fmt.Errorf("build update_field payload for %s.%s: %w", nodeID, field, err)
	}
	if err := emitEvent(ctx, tx, emitParams{
		NodeID:      nodeID,
		ProjectCode: project,
		OpType:      model.OpUpdateField,
		Payload:     payload,
		WallClockTS: wallTS,
	}); err != nil {
		return fmt.Errorf("emit update_field %s.%s: %w", nodeID, field, err)
	}
	return nil
}

// emitBackfillAnnotations walks the JSON array stored in nodes.annotations
// and emits one comment event per entry. Metadata retains the original time;
// missing or invalid time uses the already-parsed node creation time directly.
// The envelope uses a nonnegative wall clock independently of that metadata.
func emitBackfillAnnotations(ctx context.Context, tx *sql.Tx, nodeID, project, creator, rawJSON string, fallbackAt time.Time) (int, error) {
	if rawJSON == "" || rawJSON == "[]" || rawJSON == "null" {
		return 0, nil
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &entries); err != nil {
		return 0, fmt.Errorf("annotations malformed for %s: %w", nodeID, err)
	}
	count := 0
	for _, entry := range entries {
		text, _ := entry["text"].(string)
		if text == "" {
			continue
		}
		eventID, err := clock.NewEventID()
		if err != nil {
			return count, fmt.Errorf("allocate backfill annotation identity: %w", err)
		}
		value, at := backfillAnnotationPayload(entry, eventID, fallbackAt)
		payload, err := model.EncodePayload(value)
		if err != nil {
			return count, fmt.Errorf("build comment payload for %s: %w", nodeID, err)
		}
		wallTS := at.UnixMilli()
		if wallTS < 0 {
			wallTS = fallbackAt.UnixMilli()
		} // The envelope must be nonnegative; metadata keeps the original time.
		if err := emitEvent(ctx, tx, emitParams{NodeID: nodeID, ProjectCode: project, OpType: model.OpComment, Author: creator, Payload: payload, WallClockTS: wallTS, EventID: eventID}); err != nil {
			return count, fmt.Errorf("emit comment for %s: %w", nodeID, err)
		}
		count++
	}
	return count, nil
}

// Payload attribution comes from the annotation, independently of the event
// envelope author. Only absent original identity/time needs a fallback.
func backfillAnnotationPayload(entry map[string]any, eventID string, fallbackAt time.Time) (*model.CommentPayload, time.Time) {
	id, _ := entry["id"].(string)
	if id == "" {
		id = eventID
	}
	text, _ := entry["text"].(string)
	author, _ := entry["author"].(string)
	addressee, _ := entry["addressee"].(string)
	resolved, _ := entry["resolved"].(bool)
	at := fallbackAt
	if raw, ok := entry["created_at"].(string); ok {
		if parsed, parseErr := time.Parse(time.RFC3339Nano, raw); parseErr == nil {
			at = parsed
		}
	}
	return &model.CommentPayload{AuthorID: author, Body: text, Annotation: &model.CommentAnnotationState{ID: id, CreatedAt: at, Resolved: resolved, Addressee: addressee}}, at
}

func backfillNeedsTransition(status string, hasClosedAt bool) bool {
	return hasClosedAt || (status != "" && status != string(model.StatusOpen))
}
