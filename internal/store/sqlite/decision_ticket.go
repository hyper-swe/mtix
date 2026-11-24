// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// DeriveDecisionTicket snapshots the request's display-path ancestry in the
// raise writer's transaction (ADR-008 §3.5, FR-1.1a). The caller rolls back
// on error so a refused raise cannot leave behind a partial identity change.
func DeriveDecisionTicket(ctx context.Context, tx *sql.Tx, id string) (model.DecisionTicket, error) {
	uid, err := decisionSubjectUID(ctx, tx, id)
	if err != nil {
		return model.DecisionTicket{}, err
	}
	ticket := model.DecisionTicket{
		UID: uid, Ancestors: []string{}, Paths: []string{id}, PathMatch: []bool{true},
	}
	for path := model.ParseIDParent(id); path != ""; path = model.ParseIDParent(path) {
		if len(ticket.Ancestors) == 64 {
			ticket.Truncated = true
			break
		}
		// This lookup includes soft-deleted rows and returns "" for a purged link.
		parentUID, err := resolveEmitUID(ctx, tx, path)
		if err != nil {
			return model.DecisionTicket{}, fmt.Errorf("read decision ancestor %q: %w", path, err)
		}
		ticket.Ancestors = append(ticket.Ancestors, parentUID)
		ticket.Paths = append(ticket.Paths, path)
		ticket.PathMatch = append(ticket.PathMatch, true)
	}
	if err := maskHeldDeletePaths(ctx, tx, &ticket); err != nil {
		return model.DecisionTicket{}, err
	}
	return ticket, nil
}

// decisionSubjectUID requires a local row and a durable identity for the ticket itself.
func decisionSubjectUID(ctx context.Context, tx *sql.Tx, id string) (string, error) {
	uid, err := resolveEmitUID(ctx, tx, id)
	if err != nil {
		return "", fmt.Errorf("read decision ticket %q: %w", id, err)
	}
	if uid != "" {
		return uid, nil
	}
	filled, err := setBackfillUID(ctx, tx, id)
	if err != nil {
		return "", err
	}
	if !filled {
		return "", fmt.Errorf("backfill decision ticket %q: %w", id, model.ErrNotFound)
	}
	uid, err = resolveEmitUID(ctx, tx, id)
	if err != nil {
		return "", fmt.Errorf("read backfilled decision ticket %q: %w", id, err)
	}
	if uid == "" {
		return "", fmt.Errorf("decision ticket %q still lacks a uid: %w", id, model.ErrInvalidInput)
	}
	return uid, nil
}

// maskHeldDeletePaths disables path fallback only for eligible path-only
// matches already present at raise. A future unseen delete can still match.
func maskHeldDeletePaths(ctx context.Context, tx *sql.Tx, ticket *model.DecisionTicket) (err error) {
	paths, err := hexIDList(ticket.Paths)
	if err != nil {
		return err
	}
	// Check the immutable delete path (envelope ID on legacy deletes). Bind the
	// bounded chain once, so the journal is scanned only once per raise.
	rows, err := tx.QueryContext(ctx, `SELECT COALESCE(json_extract(payload,'$.path'),node_id),
			COALESCE(uid,''), json_type(payload,'$.cascade') IS 'true'
		FROM sync_events WHERE op_type='delete'
		  AND COALESCE(json_extract(payload,'$.path'),node_id) IN
		    (SELECT CAST(unhex(value) AS TEXT) FROM json_each(?))`, paths)
	if err != nil {
		return fmt.Errorf("query held decision deletes: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close held decision deletes: %w", closeErr)
		}
	}()
	indices := make(map[string]int, len(ticket.Paths))
	for i, path := range ticket.Paths {
		indices[path] = i
	}
	for rows.Next() {
		var path, deletedUID string
		var cascade bool
		if err := rows.Scan(&path, &deletedUID, &cascade); err != nil {
			return fmt.Errorf("read held decision delete: %w", err)
		}
		i := indices[path]
		if heldDeleteMasksPath(ticket, i, deletedUID, cascade) {
			ticket.PathMatch[i] = false
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate held decision deletes: %w", err)
	}
	return nil
}

// heldDeleteMasksPath recognizes a held delete that could match this path
// despite having no shared identity with the local row (ADR-008 §3.5).
func heldDeleteMasksPath(ticket *model.DecisionTicket, i int, deletedUID string, cascade bool) bool {
	uid := ticket.UID
	if i > 0 {
		if !cascade {
			return false
		}
		uid = ticket.Ancestors[i-1]
	}
	if uid == "" || deletedUID == "" {
		return true
	}
	return uid != deletedUID && (model.IsBackfillUID(uid) || model.IsBackfillUID(deletedUID))
}
