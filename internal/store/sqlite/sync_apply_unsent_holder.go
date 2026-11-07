// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/model"
)

// A merge on a store that syncs writes a task its author has not pushed, with
// no create in the sync log (MTIX-112.17.16.16, ruling 1). The hub can give
// that number to another author's task first. The create that arrives is the
// hub's, so the local task, which no peer knows, moves to the next free number
// and the hub's task takes the number: first-writer-wins, with nothing lost.

// maxUnsentMoveAttempts bounds the search for a free number, as
// RenumberForHubRejection bounds its own.
const maxUnsentMoveAttempts = 10000

// moveUnsentHolder moves the local task that holds the number of create e to the
// next free sibling number, when that task has another uid and the sync log
// holds no creation of it (neither a create_node row nor an applied create). It
// reports whether it moved the task. A holder whose creation is pending is the
// existing pending-number case (refuseHeldNumber); a holder with the same uid
// is the same task. The move journals nothing: no event names the task.
func moveUnsentHolder(ctx context.Context, tx *sql.Tx, s *Store, e *model.SyncEvent, uid string) (bool, error) {
	if s == nil {
		return false, nil
	}
	var holder string
	// The uid of the local task on the number the create names.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, e.NodeID).Scan(&holder); err != nil {
		return false, fmt.Errorf("apply create_node %s: read holder of %s: %w", format.EventID(e.EventID), e.NodeID, err)
	}
	if holder == "" || holder == uid {
		return false, nil
	}
	var inLog bool
	// A create_node row other than the incoming one, under the holder's uid (or
	// its number when the row has no uid), or one applied under its uid.
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM sync_events
		WHERE op_type = 'create_node' AND event_id != ?
		  AND ((uid = ? AND uid <> '') OR event_id = ? OR (node_id = ? AND COALESCE(uid, '') = '')))
		OR EXISTS (SELECT 1 FROM applied_events WHERE event_id = ?)`,
		e.EventID, holder, holder, e.NodeID, holder).Scan(&inLog); err != nil {
		return false, fmt.Errorf("apply create_node %s: check the log for %s: %w", format.EventID(e.EventID), e.NodeID, err)
	}
	if inLog {
		return false, nil
	}
	return true, moveToFreeNumber(ctx, tx, s, e.NodeID)
}

// moveToFreeNumber renumbers task id and its subtree to the next free sibling
// number inside tx, recording the move in the task's history.
func moveToFreeNumber(ctx context.Context, tx *sql.Tx, s *Store, id string) error {
	node, err := lockAnyNodeForRenumber(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("move %s off a number the hub gave to another task: %w", id, err)
	}
	for attempt := 0; attempt < maxUnsentMoveAttempts; attempt++ {
		seq, claimErr := nextSequenceQuery(ctx, tx, sequenceKey(node.project, node.parentID))
		if claimErr != nil {
			return fmt.Errorf("move %s: claim a free number: %w", id, claimErr)
		}
		err = s.renumberRecorded(ctx, tx, id, node, seq, model.HistoryOriginSystem)
		if err == nil {
			return nil
		}
		if !errors.Is(err, model.ErrAlreadyExists) {
			return fmt.Errorf("move %s to number %d: %w", id, seq, err)
		}
	}
	return fmt.Errorf("move %s: no free sibling number after %d attempts", id, maxUnsentMoveAttempts)
}
