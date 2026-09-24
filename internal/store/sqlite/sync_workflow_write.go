// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	"github.com/hyper-swe/mtix/internal/model"
)

// Writing a resolved workflow winner row (MTIX-95.10). The table and its
// resolution live in sync_workflow_winner.go; ingest (applyWorkflowWinner)
// and status repair (MTIX-95.6, sync_repair_status.go) both write through
// writeWorkflowColumns.

// writeWorkflowColumns writes a resolved table row onto node id (MTIX-95.10).
// A soft-deleted or missing node matches no row and is left alone.
func writeWorkflowColumns(ctx context.Context, tx *sql.Tx, id string, w workflowWrite) error {
	// One fixed statement, every value bound: status and updated_at always;
	// each other column only when the row writes it (the CASE keeps the
	// stored value otherwise).
	_, err := tx.ExecContext(ctx, `
		UPDATE nodes SET
		  status          = ?,
		  updated_at      = ?,
		  assignee        = CASE WHEN ? THEN ? ELSE assignee END,
		  agent_state     = CASE WHEN ? THEN ? ELSE agent_state END,
		  previous_status = CASE WHEN ? THEN ? ELSE previous_status END,
		  closed_at       = CASE WHEN ? THEN ? ELSE closed_at END,
		  progress        = CASE WHEN ? THEN ? ELSE progress END,
		  defer_until     = CASE WHEN ? THEN ? ELSE defer_until END
		WHERE id = ? AND deleted_at IS NULL`,
		string(w.status), w.updatedAt,
		w.assignee.write, w.assignee.value,
		w.agentState.write, w.agentState.value,
		w.previousStatus.write, w.previousStatus.value,
		w.closedAt.write, w.closedAt.value,
		w.progress.write, w.progress.value,
		w.deferUntil.write, w.deferUntil.value,
		id,
	)
	if err != nil {
		return fmt.Errorf("write workflow columns of %s: %w", id, err)
	}
	return nil
}

// applyWorkflowWinner writes a winning workflow event's table row onto the
// node the event addresses and returns that node's current id (MTIX-95.10).
// applyTransitionStatus, applyClaim, applyUnclaim and applyDefer
// (sync_apply.go) call it after decoding their payload, and are reached only
// for an event that won (dispatchWithLWW). They pass the event's
// wall_clock_ts, so a terminal closed_at is the same on every replica
// whatever order the node's workflow events arrived in, and their apply time
// as updated_at.
func applyWorkflowWinner(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, in workflowInput) (string, error) {
	id, err := resolveNodeRef(ctx, tx, e)
	if err != nil {
		return "", err
	}
	w, known := resolveWorkflowWrite(in)
	if !known {
		slog.Default().Warn("sync apply: unknown status in transition_status; wrote the status column only",
			"event_id", e.EventID, "status", string(in.to))
	}
	if err := writeWorkflowColumns(ctx, tx, id, w); err != nil {
		return "", fmt.Errorf("apply %s %s: %w", e.OpType, e.EventID, err)
	}
	return id, nil
}
