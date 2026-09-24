// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// CancelNode cancels a node with mandatory reason per FR-6.3.
// If cascade is true, all descendants are also canceled.
// Recalculates progress excluding canceled nodes from denominator (FR-5.4).
//
// Returns ErrInvalidInput if reason is empty.
// Returns ErrNotFound if the node does not exist.
// Returns ErrInvalidTransition if the transition is not allowed.
func (s *Store) CancelNode(ctx context.Context, id, reason, author string, cascade bool) error {
	if reason == "" {
		return fmt.Errorf("cancel reason is required: %w", model.ErrInvalidInput)
	}

	return s.WithTx(ctx, func(tx *sql.Tx) error {
		return executeCancelTx(ctx, tx, id, reason, author, cascade)
	})
}

// executeCancelTx performs the cancel operation within a transaction.
//
// A single-node cancel (cascade=false) cancels id, emits its
// transition_status event, recomputes its parent chain (FR-5.7) and
// unblocks its own dependents (FR-3.8). A cascade (FR-6.3) also cancels the
// non-terminal descendants and unblocks their dependents (cascadeCancel),
// then recomputes the progress of every ancestor of a cancelled descendant
// inside the subtree, the root included, once each and deepest first,
// before the root's parent chain, all in this transaction (MTIX-95.21).
func executeCancelTx(ctx context.Context, tx *sql.Tx, id, reason, author string, cascade bool) error {
	fromStatus, parentID, err := readNodeForCancel(ctx, tx, id)
	if err != nil {
		return err
	}

	if validErr := model.ValidateTransition(fromStatus, model.StatusCancelled); validErr != nil {
		return validErr
	}

	now := time.Now().UTC()
	nowStr := now.Format(time.RFC3339)

	if err := applyCancelUpdate(ctx, tx, id, fromStatus, reason, author, now, nowStr); err != nil {
		return err
	}

	payload, _ := model.EncodePayload(&model.TransitionStatusPayload{
		From:   fromStatus,
		To:     model.StatusCancelled,
		Reason: reason,
	})
	if err := emitEvent(ctx, tx, emitParams{
		NodeID:      id,
		ProjectCode: projectPrefixFromNodeID(id),
		OpType:      model.OpTransitionStatus,
		Author:      author,
		Payload:     payload,
	}); err != nil {
		return err
	}

	if cascade {
		cancelled, err := cascadeCancel(ctx, tx, id, author, nowStr)
		if err != nil {
			return fmt.Errorf("cascade cancel from %s: %w", id, err)
		}
		// MTIX-95.21: the subtree's own ancestors first (the root's parent
		// chain follows below), so each is recomputed after its children.
		if err := recomputeCascadeProgress(ctx, tx, id, cancelled); err != nil {
			return fmt.Errorf("recalculate progress after cascade cancel from %s: %w", id, err)
		}
	}

	if parentID != "" {
		if err := recalculateProgress(ctx, tx, parentID); err != nil {
			return fmt.Errorf("recalculate progress after cancel: %w", err)
		}
	}

	// MTIX-17 / FR-3.8: cancellation resolves any `blocks` deps where
	// this node is the blocker. Auto-unblock dependents. Mirrors the
	// hook in executeTransitionTx so the cancel path produces the
	// same unblock semantics as a done transition.
	if err := unblockDependents(ctx, tx, id, author); err != nil {
		return fmt.Errorf("auto-unblock dependents of cancelled %s: %w", id, err)
	}

	return nil
}

// readNodeForCancel reads the current status and parent ID for cancel validation.
func readNodeForCancel(ctx context.Context, tx *sql.Tx, id string) (model.Status, string, error) {
	var currentStatus, parentID sql.NullString
	err := tx.QueryRowContext(ctx,
		`SELECT status, parent_id FROM nodes
		 WHERE id = ? AND deleted_at IS NULL`,
		id,
	).Scan(&currentStatus, &parentID)
	if err == sql.ErrNoRows {
		return "", "", fmt.Errorf("node %s: %w", id, model.ErrNotFound)
	}
	if err != nil {
		return "", "", fmt.Errorf("read node %s for cancel: %w", id, err)
	}
	return model.Status(currentStatus.String), parentID.String, nil
}

// applyCancelUpdate sets the node to canceled and records the activity entry.
func applyCancelUpdate(ctx context.Context, tx *sql.Tx, id string, fromStatus model.Status, reason, author string, now time.Time, nowStr string) error {
	_, err := tx.ExecContext(ctx,
		`UPDATE nodes SET status = ?, closed_at = ?, updated_at = ?
		 WHERE id = ? AND deleted_at IS NULL`,
		string(model.StatusCancelled), nowStr, nowStr, id,
	)
	if err != nil {
		return fmt.Errorf("cancel node %s: %w", id, err)
	}

	if err := appendActivityEntry(ctx, tx, id, model.ActivityEntry{
		ID:        fmt.Sprintf("act-%d", now.UnixNano()),
		Type:      model.ActivityTypeStatusChange,
		Author:    author,
		Text:      reason,
		CreatedAt: now,
		Metadata:  mustMarshal(map[string]string{"from_status": string(fromStatus), "to_status": string(model.StatusCancelled)}),
	}); err != nil {
		return fmt.Errorf("record cancel activity for %s: %w", id, err)
	}

	return nil
}

// cancelledNode is a descendant that a cascade cancel moved to cancelled.
type cancelledNode struct {
	id       string
	parentID string
}

// cascadeCancel cancels all non-terminal descendants of rootID per FR-6.3
// and returns them. It uses a LIKE pattern on dot-notation IDs for subtree
// selection. The root ID is escaped (escapeLIKEPrefix) so a '_' in its
// project prefix matches literally and never reaches another project
// (MTIX-95.17).
//
// After the update it unblocks the dependents of every descendant it
// cancelled (FR-3.8, MTIX-95.21), as a single-node cancel of each would. A
// dependent blocked by several descendants therefore sees all of them
// cancelled, and a dependent inside the subtree is cancelled by then and
// stays so (autoUnblockNode restores only blocked nodes).
//
// Sync (0.5.x): the cascade emits no per-descendant sync events (ADR-006
// D10; per-descendant cancel events are OD-2, for v2). Other replicas
// receive only the root's transition_status (emitted by executeCancelTx)
// and the transition_status that autoUnblockNode emits for each dependent
// it restores. They never see the descendants cancelled: there the
// descendants keep their previous status, and a dependent restored here
// shows unblocked while the descendant that blocked it is not cancelled.
func cascadeCancel(ctx context.Context, tx *sql.Tx, rootID, author, nowStr string) ([]cancelledNode, error) {
	cancelled, err := cancelDescendants(ctx, tx, rootID, nowStr)
	if err != nil {
		return nil, err
	}
	for _, n := range cancelled {
		if err := unblockDependents(ctx, tx, n.id, author); err != nil {
			return nil, fmt.Errorf("auto-unblock dependents of cascade-cancelled %s: %w", n.id, err)
		}
	}
	return cancelled, nil
}

// cancelDescendants sets every live, non-terminal descendant of rootID to
// cancelled and returns the id and parent_id of each row it changed
// (FR-6.3, MTIX-95.21).
func cancelDescendants(ctx context.Context, tx *sql.Tx, rootID, nowStr string) (nodes []cancelledNode, err error) {
	// Cancel only non-terminal descendants: every id that starts with the
	// literal "<rootID>." (escaped prefix, parameterized, ESCAPE '\'), and
	// return each changed row so the caller can unblock and recompute for it.
	rows, err := tx.QueryContext(ctx,
		`UPDATE nodes SET status = ?, closed_at = ?, updated_at = ?
		 WHERE id LIKE ? ESCAPE '\'
		   AND deleted_at IS NULL
		   AND status NOT IN (?, ?, ?)
		 RETURNING id, parent_id`,
		string(model.StatusCancelled), nowStr, nowStr,
		escapeLIKEPrefix(rootID)+".%",
		string(model.StatusDone), string(model.StatusCancelled), string(model.StatusInvalidated),
	)
	if err != nil {
		return nil, fmt.Errorf("cascade cancel descendants of %s: %w", rootID, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close cascade cancel rows of %s: %w", rootID, closeErr)
		}
	}()
	for rows.Next() {
		var n cancelledNode
		var parentID sql.NullString
		if scanErr := rows.Scan(&n.id, &parentID); scanErr != nil {
			return nil, fmt.Errorf("scan cascade-cancelled descendant of %s: %w", rootID, scanErr)
		}
		n.parentID = parentID.String
		nodes = append(nodes, n)
	}
	// Defensive: the driver reports an aborted UPDATE ... RETURNING (a
	// constraint or trigger failure) from QueryContext above, not here.
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("cascade cancel descendants of %s: %w", rootID, iterErr)
	}
	return nodes, nil
}

// recomputeCascadeProgress recomputes, once each, the progress of every
// ancestor of a cascade-cancelled node inside the subtree of rootID (the
// root included), deepest first, without walking above the root (FR-5.4,
// FR-5.7, MTIX-95.21). recalculateProgress walks to the top on every call,
// so calling it per descendant would recompute shared ancestors repeatedly;
// the caller runs it once from the root's parent afterwards.
func recomputeCascadeProgress(ctx context.Context, tx *sql.Tx, rootID string, cancelled []cancelledNode) error {
	for _, id := range cascadeProgressTargets(rootID, cancelled) {
		if err := recomputeNodeProgress(ctx, tx, id); err != nil {
			return fmt.Errorf("recompute progress of %s: %w", id, err)
		}
	}
	return nil
}

// cascadeProgressTargets returns, deepest first, each distinct node whose
// progress a cascade cancel from rootID changes: every ancestor of a
// cancelled node up to and including rootID (MTIX-95.21). The walk starts
// at the stored parent_id and continues through the dot-notation parent
// (model.ParseIDParent, the same path the subtree LIKE selects on), so an
// ancestor that was not cancelled itself, such as a done intermediate node,
// is included too. Nodes outside the subtree are never returned. Ties at
// one depth are ordered by id so the order is deterministic.
func cascadeProgressTargets(rootID string, cancelled []cancelledNode) []string {
	seen := make(map[string]bool, len(cancelled))
	var targets []string
	for _, n := range cancelled {
		for p := n.parentID; inSubtree(rootID, p) && !seen[p]; p = model.ParseIDParent(p) {
			seen[p] = true
			targets = append(targets, p)
		}
	}
	sort.Slice(targets, func(i, j int) bool {
		di, dj := strings.Count(targets[i], "."), strings.Count(targets[j], ".")
		if di != dj {
			return di > dj
		}
		return targets[i] < targets[j]
	})
	return targets
}

// inSubtree reports whether id is rootID or one of its dot-notation
// descendants.
func inSubtree(rootID, id string) bool {
	return id == rootID || strings.HasPrefix(id, rootID+".")
}
