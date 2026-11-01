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

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// fromIDForDepEdge resolves the source node of a dependency edge through
// the uid-aware ref (ADR-003 §3) so an edge survives a renumber of the
// from-node. Dependency ops are idempotent and key on the (from,to,type)
// tuple, not on node existence (the FK already guards a missing node), so
// a NotFound from resolveNodeRef is tolerated by falling back to the
// event's node_id — preserving the pre-30.6 behavior for uid-less events.
func fromIDForDepEdge(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (string, error) {
	id, err := resolveNodeRef(ctx, tx, e)
	if err == nil {
		return id, nil
	}
	if errors.Is(err, ErrNumberHeldByPendingCreate) {
		return "", err
	}
	// The fallback names the task by number alone: the same guard applies.
	if err := refusePendingNumber(ctx, tx, e.EventID, e.NodeID); err != nil {
		return "", err
	}
	return e.NodeID, nil
}

// applyDelete is a tombstone. SYNC-DESIGN section 8.3: delete on a
// non-existent node is a no-op (no phantom tombstone). Existing nodes
// get deleted_at set; this does NOT cascade — any descendants must
// have their own delete events.
func (a syncApplier) applyDelete(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	// Resolve the target through the uid-aware ref so a delete still finds
	// a renumbered node by uid. SYNC-DESIGN §8.3: delete on a non-existent
	// (or already-deleted) node is a no-op — NOT an error — so we swallow
	// ErrNotFound here rather than surfacing it.
	id, err := resolveNodeRef(ctx, tx, e)
	if errors.Is(err, model.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	now := a.clock().UTC().Format(time.RFC3339)
	if _, err = tx.ExecContext(ctx,
		`UPDATE nodes SET deleted_at = ?, deleted_by = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		now, e.AuthorID, now, id,
	); err != nil {
		return err
	}

	// MTIX-44: mirror the local DeleteNode parent-progress rollup
	// (node_delete.go, FR-5.7). A deleted child is excluded from the parent's
	// denominator, so the parent's progress must be recomputed; without this
	// a delete synced in from another client leaves the local parent's
	// progress stale and divergent. Uses the resolved (current) id so a
	// renumber is honored. Sync deletes do NOT cascade (each descendant
	// carries its own delete event), matching applyDelete's contract.
	if parentID := model.ParseIDParent(id); parentID != "" {
		if err := recalculateProgress(ctx, tx, parentID); err != nil {
			return fmt.Errorf("apply delete %s: recalc progress: %w", format.EventID(e.EventID), err)
		}
	}
	return nil
}

func (a syncApplier) applySetAcceptance(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.SetAcceptancePayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply set_acceptance %s: decode payload: %w", format.EventID(e.EventID), err)
	}
	id, err := resolveNodeRef(ctx, tx, e)
	if err != nil {
		return err
	}
	now := a.clock().UTC().Format(time.RFC3339)
	if _, err = tx.ExecContext(ctx,
		`UPDATE nodes SET acceptance = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		p.AcceptanceText, now, id,
	); err != nil {
		return err
	}
	return recomputeContentHashRow(ctx, tx, id)
}

func (a syncApplier) applySetPrompt(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) error {
	var p model.SetPromptPayload
	if err := json.Unmarshal(e.Payload, &p); err != nil {
		return fmt.Errorf("apply set_prompt %s: decode payload: %w", format.EventID(e.EventID), err)
	}
	id, err := resolveNodeRef(ctx, tx, e)
	if err != nil {
		return err
	}
	now := a.clock().UTC().Format(time.RFC3339)
	if _, err = tx.ExecContext(ctx,
		`UPDATE nodes SET prompt = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		p.PromptText, now, id,
	); err != nil {
		return err
	}
	return recomputeContentHashRow(ctx, tx, id)
}

// recomputeContentHashRow recomputes and stores content_hash from a node's
// CURRENT (already-mutated) content fields. Sync-apply handlers call this after
// a content mutation so a synced edit maintains content_hash exactly like local
// UpdateNode does (FR-3.7) — the local write path recomputes it (node_update.go)
// but the apply path historically skipped it, leaving a stale hash on synced
// content edits and NULL on synced-created nodes (MTIX-46). ComputeContentHash
// is deterministic over content, so a replica converges on the SAME content_hash
// the emitting replica stored, keeping export bytes and import-merge identity
// detection (FR-7.8) consistent across replicas. It does NOT touch updated_at
// (the caller's mutation owns that) and is a no-op for a missing/soft-deleted
// row (the mutation's WHERE guard matched no row, so there is nothing to hash).
func recomputeContentHashRow(ctx context.Context, tx *sql.Tx, id string) error {
	var title, description, prompt, acceptance, labelsJSON sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT title, description, prompt, acceptance, labels
		 FROM nodes WHERE id = ? AND deleted_at IS NULL`,
		id,
	).Scan(&title, &description, &prompt, &acceptance, &labelsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("recompute content_hash: read content fields for %s: %w", id, err)
	}

	var labels []string
	if labelsJSON.Valid && labelsJSON.String != "" {
		if err := json.Unmarshal([]byte(labelsJSON.String), &labels); err != nil {
			return fmt.Errorf("recompute content_hash: unmarshal labels for %s: %w", id, err)
		}
	}

	hash := model.ComputeContentHash(title.String, description.String, prompt.String, acceptance.String, labels)
	if _, err := tx.ExecContext(ctx,
		`UPDATE nodes SET content_hash = ? WHERE id = ?`, hash, id,
	); err != nil {
		return fmt.Errorf("recompute content_hash: store for %s: %w", id, err)
	}
	return nil
}

// resolveNodeRef returns the dot-path id of the live node an event acts on
// (ADR-003 §3, §7 Phase 3 — the single keying choke point for apply).
//
// Keying rule (dual-carry transition):
//   - When the event carries a uid, the node is looked up by uid
//     (uid-AUTHORITATIVE). This is what makes a renumber touch ZERO
//     events: the display path (node_id) may have moved, but the uid is
//     stable, so the event still finds the node. The returned id is the
//     node's CURRENT display path, which the caller binds into its
//     UPDATE/SELECT — never trusting the possibly-stale e.NodeID.
//   - When the event carries NO uid (an old CLI's event, ADR-003 §7
//     Phase 3), apply falls back to node_id exactly as before.
//
// Returns model.ErrNotFound (wrapped) if no live node matches — including
// HAZARD (c): an event whose node's create_node has not yet applied
// (causal order) surfaces ErrNotFound rather than silently mis-applying.
// All applyXxx functions that target an existing row route through this so
// the fallback is not duplicated at ~12 sites.
func resolveNodeRef(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) (string, error) {
	var (
		id    string
		err   error
		query = `SELECT id FROM nodes WHERE id = ? AND deleted_at IS NULL`
		arg   = e.NodeID
	)
	if e.UID != "" {
		// The live node holding the event's uid; "uid <> ''" lets the partial
		// idx_nodes_uid serve it, not a read of every node (MTIX-95.47).
		query = `SELECT id FROM nodes WHERE uid = ? AND uid <> '' AND deleted_at IS NULL`
		arg = e.UID
	}
	err = tx.QueryRowContext(ctx, query, arg).Scan(&id)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("node %s (uid=%s): %w", e.NodeID, format.EventID(e.UID), model.ErrNotFound)
	}
	if err != nil {
		return "", err
	}
	if e.UID == "" {
		// An event without a uid names its task by number alone: refuse a
		// number held by a task whose creation is still pending here, which
		// the event is not about (MTIX-95.37).
		if err := refusePendingNumber(ctx, tx, e.EventID, id); err != nil {
			return "", err
		}
	}
	return id, nil
}

// dispatchApply retains the free apply seam for existing package callers.
func dispatchApply(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	return (syncApplier{clock: time.Now}).dispatchApply(ctx, tx, event)
}

// mirrorIncomingEvent retains the existing journal test seam.
func mirrorIncomingEvent(ctx context.Context, tx *sql.Tx, event *model.SyncEvent) error {
	return (syncApplier{clock: time.Now}).mirrorIncomingEvent(ctx, tx, event)
}

// appliedCreateRow is the decoded, clock-stamped create row.
type appliedCreateRow struct {
	payload         model.CreateNodePayload
	uid, labels, at string
}

// insertAppliedCreate preserves the existing number-conflict and UID checks.
func insertAppliedCreate(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, row appliedCreateRow) error {
	p, uid, labelsJSON, now := row.payload, row.uid, row.labels, row.at
	depth := computeDepth(p.ParentID)
	canonical := model.NodeTypeForDepth(depth)
	res, err := tx.ExecContext(ctx, `
		INSERT INTO nodes
		  (id, uid, parent_id, depth, seq, project,
		   title, description, prompt, acceptance,
		   node_type, issue_type, priority, labels,
		   status, progress,
		   assignee, creator,
		   created_at, updated_at,
		   weight,
		   activity, annotations)
		VALUES (?, ?, ?, ?, ?, ?, ?,
		        ?, ?, ?, ?,
		        ?, ?, ?,
		        ?, ?,
		        ?, ?,
		        ?, ?,
		        ?,
		        '[]', '[]')
		ON CONFLICT (id) DO NOTHING`,
		e.NodeID, uid, nullableString(p.ParentID), depth, deriveSeq(e.NodeID), e.ProjectPrefix,
		p.Title, nullableString(p.Description), nullableString(p.Prompt), nullableString(p.Acceptance),
		string(canonical), nullableString(string(p.IssueType)), int(p.Priority), labelsJSON,
		string(model.StatusOpen), 0.0,
		nullableString(p.Assignee), nullableString(p.Creator),
		now, now,
		1.0,
	)
	if err != nil {
		return fmt.Errorf("apply create_node %s: insert node %s: %w", format.EventID(e.EventID), e.NodeID, err)
	}
	if inserted, rowsErr := res.RowsAffected(); rowsErr != nil {
		return fmt.Errorf("apply create_node %s: rows affected: %w", format.EventID(e.EventID), rowsErr)
	} else if inserted == 0 {
		if err := refuseHeldNumber(ctx, tx, e, uid); err != nil {
			return err
		}
	}

	return nil
}
