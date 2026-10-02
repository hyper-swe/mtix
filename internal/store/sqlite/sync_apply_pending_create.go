// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// Apply-side guards for a number held by a task whose creation is still
// pending here (MTIX-95.37). The hub has not granted that task its number
// yet and may give it another, so a teammate's event that names the number
// is about the teammate's task, never about the local one.

// ErrNumberHeldByPendingCreate is returned by apply for an event that names,
// by number, a task whose own creation is still pending here, or one of its
// descendants (MTIX-95.37). It is not ErrNotFound, which some ops treat as
// "nothing to do": the event is refused and quarantined, and the retry applies
// it once the local task has moved to the number the hub granted it.
var ErrNumberHeldByPendingCreate = errors.New("number held by a task whose creation is not on the hub yet")

// refusePendingNumber returns ErrNumberHeldByPendingCreate (wrapped, naming
// the event and the number) when ref, a number an event names, is held by a
// task made here whose create_node is pending, or lies below such a task. An
// empty ref names nothing. It is the one check every place where apply
// resolves a node by number goes through: a node's own number, a creation's
// parent_id, a link's or unlink's depends_on_node_id and the source node of a
// link found by number.
func refusePendingNumber(ctx context.Context, tx *sql.Tx, eventID, ref string) error {
	line := nodeLineOf(ref)
	if len(line) == 0 {
		return nil
	}
	pending, err := pendingCreationIDs(ctx, tx, line)
	if err != nil {
		return fmt.Errorf("event %s: check %s: %w", eventID, ref, err)
	}
	if len(pending) > 0 {
		return fmt.Errorf("event %s: %s: %w", eventID, ref, ErrNumberHeldByPendingCreate)
	}
	return nil
}

// refuseHeldNumber refuses a create_node whose number is held, here, by a
// task whose own creation is still pending (MTIX-95.37): the INSERT above
// did nothing for it, and without this the creation would count as applied
// and its task would never appear once the local task moved to a new number.
// The error quarantines the event, which the next pulls retry. A number held
// by any other task, as by a creation applied earlier, keeps the documented
// first-writer-wins no-op.
func refuseHeldNumber(ctx context.Context, tx *sql.Tx, e *model.SyncEvent, uid string) error {
	var holder string
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, e.NodeID).Scan(&holder)
	if err != nil {
		return fmt.Errorf("apply create_node %s: read holder of %s: %w", e.EventID, e.NodeID, err)
	}
	if holder == uid {
		return nil // inserted just now, or the same task again
	}
	return refusePendingNumber(ctx, tx, e.EventID, e.NodeID)
}
