// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// BackfillResult reports what mtix sync backfill emitted (or would
// emit, under --dry-run).
type BackfillResult struct {
	NodeCount       int `json:"node_count"`        // walked
	CreateEvents    int `json:"create_events"`     // create_node emitted
	UpdateFieldEvents int `json:"update_field_events"`
	TransitionEvents  int `json:"transition_events"`
	AnnotateEvents    int `json:"annotate_events"`
	LinkDepEvents     int `json:"link_dep_events"`
	TotalEvents       int `json:"total_events"`
}

// ErrBackfillSyncEventsNonEmpty is returned by Backfill when sync_events
// already contains rows. Refusal-by-default is the load-bearing safety
// rail per MTIX-15.13.1 N5: re-running backfill against a project that
// has emitted events would silently duplicate the history on the hub
// (the hub dedupes by event_id; --force generates fresh IDs and
// defeats that). To re-backfill from scratch, the operator must first
// run `mtix sync reconcile --discard-local` (which wipes sync_events
// per FR-18.13).
var ErrBackfillSyncEventsNonEmpty = errors.New("sync_events table is non-empty")

// ErrBackfillNodesInvariant is returned when the canonical `nodes`
// table fails an invariant check (e.g., parent_id points at a
// non-existent row). Backfill refuses to proceed because synthesizing
// events from corrupt source data would propagate corruption to the
// hub. Operator runs `mtix verify` and fixes the underlying data
// first.
var ErrBackfillNodesInvariant = errors.New("nodes table invariant violation")

// CountSyncEvents returns the number of rows in sync_events. Used by
// the backfill CLI's refusal check.
func (s *Store) CountSyncEvents(ctx context.Context) (int, error) {
	var n int
	if err := s.readDB.QueryRowContext(ctx,
		`SELECT count(*) FROM sync_events`,
	).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sync_events: %w", err)
	}
	return n, nil
}

// BackfillDryRun walks the canonical tables and returns the counts
// that a real Backfill call would emit, WITHOUT writing anything to
// sync_events. No transaction is opened; the read pool is used.
func (s *Store) BackfillDryRun(ctx context.Context) (BackfillResult, error) {
	if err := s.verifyNodesInvariants(ctx, s.readDB); err != nil {
		return BackfillResult{}, err
	}
	return s.countBackfillWork(ctx, s.readDB)
}

// Backfill synthesizes sync_events rows for every existing node,
// annotation entry, and dependency in the local SQLite. Used by
// v0.1.x → v0.2.0-beta upgraders so their existing history flows to
// the hub on the next push.
//
// Safety properties (audited per MTIX-15.13.1 plan):
//   - Single-tx atomicity: all emits OR none. SQLite WAL rollback on
//     any failure (including process SIGKILL mid-walk per the chaos
//     test).
//   - Refusal-by-default if sync_events is non-empty (caller passes
//     force=true to override; --force is an intentional opt-in).
//   - Refusal if the nodes table fails an invariant check (parent_id
//     dangling). Caller is pointed at `mtix verify`.
//   - Lamport is monotonic in walk order (correct per the sync
//     invariants — lamport is causal, not wall-clock).
//   - wall_clock_ts is set to the source row's created_at so the
//     temporal audit trail is preserved.
//
// Returns BackfillResult on success. Returns ErrBackfillSyncEventsNonEmpty
// or ErrBackfillNodesInvariant for the two refusal paths.
func (s *Store) Backfill(ctx context.Context, force bool) (BackfillResult, error) {
	count, countErr := s.CountSyncEvents(ctx)
	if countErr != nil {
		return BackfillResult{}, countErr
	}
	if count > 0 && !force {
		return BackfillResult{}, fmt.Errorf("%w (%d events present)",
			ErrBackfillSyncEventsNonEmpty, count)
	}

	if verifyErr := s.verifyNodesInvariants(ctx, s.readDB); verifyErr != nil {
		return BackfillResult{}, verifyErr
	}

	var result BackfillResult
	txErr := s.WithTx(ctx, func(tx *sql.Tx) error {
		r, runErr := s.runBackfillInTx(ctx, tx)
		result = r
		return runErr
	})
	if txErr != nil {
		return BackfillResult{}, txErr
	}
	return result, nil
}

// backfillNodeRow holds the canonical fields plus the raw annotations
// JSON string that the emit loop needs.
type backfillNodeRow struct {
	node        *model.Node
	annotations string
}

// runBackfillInTx is the single-tx body. Called by Backfill; extracted
// so the chaos test can override and panic mid-walk. Split into three
// helpers for cognitive-complexity hygiene.
func (s *Store) runBackfillInTx(ctx context.Context, tx *sql.Tx) (BackfillResult, error) {
	var result BackfillResult

	collected, err := collectBackfillNodes(ctx, tx)
	if err != nil {
		return result, err
	}
	result.NodeCount = len(collected)

	for _, nr := range collected {
		if perNodeErr := emitBackfillForNode(ctx, tx, nr, &result); perNodeErr != nil {
			return result, perNodeErr
		}
	}

	depCount, depErr := emitBackfillDependencies(ctx, tx)
	if depErr != nil {
		return result, depErr
	}
	result.LinkDepEvents = depCount

	result.TotalEvents = result.CreateEvents + result.UpdateFieldEvents +
		result.TransitionEvents + result.AnnotateEvents + result.LinkDepEvents
	return result, nil
}

// collectBackfillNodes walks the canonical `nodes` table in
// created_at order and returns the materialized rows. Pulled out for
// cognitive-complexity hygiene; runBackfillInTx then iterates and
// emits.
func collectBackfillNodes(ctx context.Context, tx *sql.Tx) ([]backfillNodeRow, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT id, parent_id, depth, seq, project, title, description,
		       prompt, acceptance, node_type, issue_type, priority,
		       labels, status, assignee, creator, created_at, updated_at,
		       weight, annotations
		FROM nodes
		WHERE deleted_at IS NULL
		ORDER BY created_at ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("walk nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var collected []backfillNodeRow
	for rows.Next() {
		nr, scanErr := scanBackfillNode(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		collected = append(collected, nr)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("walk nodes: %w", iterErr)
	}
	if closeErr := rows.Close(); closeErr != nil {
		return nil, fmt.Errorf("close nodes rows: %w", closeErr)
	}
	return collected, nil
}

// scanBackfillNode pulls one nodes row out of *sql.Rows and assembles
// a backfillNodeRow.
func scanBackfillNode(rows *sql.Rows) (backfillNodeRow, error) {
	n := &model.Node{}
	var (
		parentID, description, prompt, acceptance sql.NullString
		issueType, labels                         sql.NullString
		assignee, creator, annotations            sql.NullString
		createdAt, updatedAt                      string
		weight                                    sql.NullFloat64
	)
	if scanErr := rows.Scan(
		&n.ID, &parentID, &n.Depth, &n.Seq, &n.Project, &n.Title,
		&description, &prompt, &acceptance, &n.NodeType, &issueType,
		&n.Priority, &labels, &n.Status, &assignee, &creator,
		&createdAt, &updatedAt, &weight, &annotations,
	); scanErr != nil {
		return backfillNodeRow{}, fmt.Errorf("scan node: %w", scanErr)
	}
	if parentID.Valid {
		n.ParentID = parentID.String
	}
	if description.Valid {
		n.Description = description.String
	}
	if prompt.Valid {
		n.Prompt = prompt.String
	}
	if acceptance.Valid {
		n.Acceptance = acceptance.String
	}
	if assignee.Valid {
		n.Assignee = assignee.String
	}
	if creator.Valid {
		n.Creator = creator.String
	}
	if weight.Valid {
		n.Weight = weight.Float64
	} else {
		n.Weight = 1.0
	}
	ts, parseErr := parseFlexibleTime(createdAt)
	if parseErr != nil {
		return backfillNodeRow{}, fmt.Errorf("parse created_at for %s: %w", n.ID, parseErr)
	}
	n.CreatedAt = ts
	uts, _ := parseFlexibleTime(updatedAt)
	n.UpdatedAt = uts

	annStr := "[]"
	if annotations.Valid {
		annStr = annotations.String
	}
	return backfillNodeRow{node: n, annotations: annStr}, nil
}

// parseFlexibleTime accepts either RFC3339Nano or RFC3339 (the two
// formats SQLite-written timestamps appear in).
func parseFlexibleTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse(time.RFC3339, s)
}

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

	if string(n.Status) != "" && n.Status != model.StatusOpen {
		if transErr := emitBackfillTransition(ctx, tx, n, updateTS); transErr != nil {
			return transErr
		}
		result.TransitionEvents++
	}

	annCount, annErr := emitBackfillAnnotations(ctx, tx, n.ID, n.Project,
		n.Creator, nr.annotations, wallTS)
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
// and emits one `annotate` event per entry. Each annotation's
// wall_clock_ts is preserved from the source entry if present;
// otherwise falls back to the node's created_at.
func emitBackfillAnnotations(ctx context.Context, tx *sql.Tx, nodeID, project, creator, rawJSON string, fallbackTS int64) (int, error) {
	if rawJSON == "" || rawJSON == "[]" || rawJSON == "null" {
		return 0, nil
	}
	var entries []map[string]any
	if err := json.Unmarshal([]byte(rawJSON), &entries); err != nil {
		// Annotations malformed — log via error path; don't propagate
		// corruption to the hub. Backfill of this node's annotations
		// is skipped; the create_node + update_field events already
		// emitted are kept.
		return 0, fmt.Errorf("annotations malformed for %s: %w", nodeID, err)
	}
	count := 0
	for _, e := range entries {
		text, _ := e["text"].(string)
		kind, _ := e["kind"].(string)
		if text == "" {
			continue
		}
		wallTS := fallbackTS
		if createdStr, ok := e["created_at"].(string); ok {
			if t, err := time.Parse(time.RFC3339Nano, createdStr); err == nil {
				wallTS = t.UnixMilli()
			}
		}
		_ = kind // annotations kind is reserved for future use; current
		// CommentPayload does not surface it.
		payload, err := model.EncodePayload(&model.CommentPayload{
			AuthorID: creator,
			Body:     text,
		})
		if err != nil {
			return count, fmt.Errorf("build comment payload for %s: %w", nodeID, err)
		}
		if err := emitEvent(ctx, tx, emitParams{
			NodeID:      nodeID,
			ProjectCode: project,
			OpType:      model.OpComment,
			Author:      creator,
			Payload:     payload,
			WallClockTS: wallTS,
		}); err != nil {
			return count, fmt.Errorf("emit comment for %s: %w", nodeID, err)
		}
		count++
	}
	return count, nil
}

// countBackfillWork performs the same walk as runBackfillInTx but only
// counts what would be emitted. Used by BackfillDryRun. Per-row work
// is delegated to accountBackfillNodeRow for cognitive-complexity
// hygiene.
func (s *Store) countBackfillWork(ctx context.Context, db queryable) (BackfillResult, error) {
	var result BackfillResult

	rows, err := db.QueryContext(ctx, `
		SELECT id, description, prompt, acceptance, assignee, status, annotations
		FROM nodes
		WHERE deleted_at IS NULL`)
	if err != nil {
		return result, fmt.Errorf("count walk nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		if scanErr := accountBackfillNodeRow(rows, &result); scanErr != nil {
			return result, scanErr
		}
	}
	if iterErr := rows.Err(); iterErr != nil {
		return result, fmt.Errorf("count walk: %w", iterErr)
	}

	var depCount int
	if depErr := db.QueryRowContext(ctx,
		`SELECT count(*) FROM dependencies`,
	).Scan(&depCount); depErr != nil {
		return result, fmt.Errorf("count deps: %w", depErr)
	}
	result.LinkDepEvents = depCount

	result.TotalEvents = result.CreateEvents + result.UpdateFieldEvents +
		result.TransitionEvents + result.AnnotateEvents + result.LinkDepEvents
	return result, nil
}

// accountBackfillNodeRow scans one nodes row in the dry-run shape and
// tallies the events that would be emitted, without touching tx.
func accountBackfillNodeRow(rows *sql.Rows, result *BackfillResult) error {
	var id string
	var description, prompt, acceptance, assignee sql.NullString
	var status string
	var annotations sql.NullString
	if scanErr := rows.Scan(&id, &description, &prompt, &acceptance,
		&assignee, &status, &annotations); scanErr != nil {
		return fmt.Errorf("scan: %w", scanErr)
	}
	result.NodeCount++
	result.CreateEvents++
	for _, s := range []sql.NullString{description, prompt, acceptance, assignee} {
		if s.Valid && s.String != "" {
			result.UpdateFieldEvents++
		}
	}
	if status != "" && status != string(model.StatusOpen) {
		result.TransitionEvents++
	}
	if annotations.Valid && annotations.String != "" &&
		annotations.String != "[]" && annotations.String != "null" {
		var entries []map[string]any
		if err := json.Unmarshal([]byte(annotations.String), &entries); err == nil {
			for _, e := range entries {
				if text, ok := e["text"].(string); ok && text != "" {
					result.AnnotateEvents++
				}
			}
		}
	}
	return nil
}

// verifyNodesInvariants checks that every non-null parent_id refers
// to an existing node row. Returns ErrBackfillNodesInvariant on
// violation. Cheap query — one JOIN with NULL check.
func (s *Store) verifyNodesInvariants(ctx context.Context, db queryable) error {
	var orphanCount int
	if err := db.QueryRowContext(ctx, `
		SELECT count(*) FROM nodes c
		WHERE c.parent_id IS NOT NULL
		  AND c.deleted_at IS NULL
		  AND NOT EXISTS (
		    SELECT 1 FROM nodes p WHERE p.id = c.parent_id
		  )`,
	).Scan(&orphanCount); err != nil {
		return fmt.Errorf("verify nodes: %w", err)
	}
	if orphanCount > 0 {
		return fmt.Errorf("%w: %d node(s) reference missing parent (run `mtix verify` to investigate)",
			ErrBackfillNodesInvariant, orphanCount)
	}
	return nil
}

// queryable is the minimal interface satisfied by both *sql.DB and
// *sql.Tx so countBackfillWork and verifyNodesInvariants can run on
// either.
type queryable interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}
