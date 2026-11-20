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

// A hub create on a number held by a local task that has no create in the log
// (MTIX-112.17.16.39). Such a task was written before sync, or its creation
// was never logged, so the hub has never been told of it and cannot have given
// it the number: the hub's task is the one the number belongs to. The local
// task moves, with its subtree, to the next free number and keeps its uid;
// both survive. A holder whose create is in the log (pending, pushed or
// applied) keeps the documented first-writer-wins no-op / pending refusal; a
// holder whose subtree the hub knows is not moved and the hub create is held.

// ErrNumberHeldByLoggedSubtree is returned by apply for a hub create whose
// number is held by a local task with no create in the log, when a task under
// it has a create the hub holds or this replica applied: the holder cannot
// move, because that task's number must stay the same on every replica. The
// error quarantines the event (it shows in sync quarantine list, sync status
// and doctor) instead of dropping the hub's task silently.
var ErrNumberHeldByLoggedSubtree = errors.New("number held by a local task whose subtree is on the hub")

// maxUnloggedMoveAttempts bounds the search for a free number, so a corrupt
// counter can never spin forever.
const maxUnloggedMoveAttempts = 10000

// holderUnloggedSQL reads the holder of a number: its uid, and whether it has
// a uid and no create_node of its own in the log under any status (matched by
// event id and by uid, the two keys a create is logged under).
const holderUnloggedSQL = `
	SELECT COALESCE(n.uid, ''),
	       (n.uid IS NOT NULL AND n.uid <> '' AND NOT EXISTS (SELECT 1 FROM sync_events c
	                                    WHERE c.op_type = 'create_node'
	                                      AND (c.event_id = n.uid OR c.uid = n.uid)))
	  FROM nodes n WHERE n.id = ?`

// subtreeLoggedSQL reports whether any task of the holder's subtree (the
// holder, its descendants, live or deleted) has a create_node in the log that
// is no longer pending: the hub holds it, or this replica applied it from the
// hub, so its number is the same on every replica and must not move. A task
// without a uid is matched by its number, except against the incoming event.
const subtreeLoggedSQL = `
	SELECT EXISTS (
	  SELECT 1 FROM nodes d JOIN sync_events c
	    ON c.op_type = 'create_node' AND c.sync_status <> 'pending'
	   AND ((d.uid IS NOT NULL AND d.uid <> '' AND (c.event_id = d.uid OR c.uid = d.uid))
	        OR (COALESCE(d.uid, '') = '' AND c.node_id = d.id AND c.event_id <> ?))
	 WHERE d.id = ? OR d.id LIKE ? ESCAPE '\')`

// moveUnloggedHolder moves the local task that holds e.NodeID, when it has a
// different non-empty uid than the incoming create's and neither it nor any
// task under it has a create_node the hub can know of (pushed or applied), to
// the next free number under its parent, inside tx. It reports whether it
// moved the task; it returns ErrNumberHeldByLoggedSubtree when a task under the
// holder is known to the hub. The pending events of the moved subtree, the children, the
// dependencies and the history follow it.
func moveUnloggedHolder(ctx context.Context, tx *sql.Tx, store *Store, e *model.SyncEvent, uid string) (bool, error) {
	var holder string
	var unlogged bool
	err := tx.QueryRowContext(ctx, holderUnloggedSQL, e.NodeID).Scan(&holder, &unlogged)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("apply create_node %s: read holder of %s: %w", format.EventID(e.EventID), e.NodeID, err)
	}
	if holder == uid || !unlogged {
		return false, nil
	}
	var logged bool
	if err := tx.QueryRowContext(ctx, subtreeLoggedSQL,
		e.EventID, e.NodeID, escapeLIKEPrefix(e.NodeID)+".%").Scan(&logged); err != nil {
		return false, fmt.Errorf("apply create_node %s: read creates under %s: %w", format.EventID(e.EventID), e.NodeID, err)
	}
	if logged {
		// The holder cannot move without renumbering a task the hub knows, and
		// the hub's task must not be dropped without a trace: hold it, with a
		// reason that names the number and the local holder.
		return false, fmt.Errorf("apply create_node %s: %s is held by local task %s, whose subtree has a creation the hub knows: %w",
			format.EventID(e.EventID), e.NodeID, holder, ErrNumberHeldByLoggedSubtree)
	}
	if err := refusePendingNumber(ctx, tx, e.EventID, e.NodeID); err != nil {
		return false, err
	}
	if err := renumberToNextFree(ctx, tx, store, e.NodeID); err != nil {
		return false, fmt.Errorf("apply create_node %s: move local task %s aside: %w", format.EventID(e.EventID), e.NodeID, err)
	}
	return true, nil
}

// renumberToNextFree moves node id and its subtree to the next free sibling
// number under its parent, and re-addresses the pending events of the moved
// subtree to the new numbers.
func renumberToNextFree(ctx context.Context, tx *sql.Tx, store *Store, id string) error {
	node, err := lockAnyNodeForRenumber(ctx, tx, id)
	if err != nil {
		return err
	}
	for attempt := 0; attempt < maxUnloggedMoveAttempts; attempt++ {
		seq, err := nextSequenceQuery(ctx, tx, sequenceKey(node.project, node.parentID))
		if err != nil {
			return err
		}
		newID := model.BuildID(node.project, node.parentID, seq)
		if err := assertTargetNamespaceFree(ctx, tx, newID); err != nil {
			if errors.Is(err, model.ErrAlreadyExists) {
				continue
			}
			return err
		}
		return moveSubtreeRecorded(ctx, tx, store, subtreeMove{oldID: id, newID: newID, node: node, seq: seq})
	}
	return fmt.Errorf("no free sibling of %s after %d attempts: %w", id, maxUnloggedMoveAttempts, model.ErrAlreadyExists)
}

// moveSubtreeRecorded renumbers mv.oldID's subtree to sibling number mv.seq,
// moves its history and re-addresses its pending events. The move is noted for
// the transaction's history tracker only once every write has succeeded, so a
// failed move leaves no phantom renumber behind; the subtree's fields are read
// into the tracker before they are rewritten.
func moveSubtreeRecorded(ctx context.Context, tx *sql.Tx, store *Store, mv subtreeMove) error {
	if err := store.touchHistorySubtree(ctx, tx, mv.oldID); err != nil {
		return err
	}
	if err := renumberSubtreeTx(ctx, tx, mv.oldID, mv.node, mv.seq); err != nil {
		return err
	}
	if err := rekeyHistorySubtree(ctx, tx, mv.oldID, mv.newID); err != nil {
		return err
	}
	if err := requeueMovedEvents(ctx, tx, mv.oldID, mv.newID); err != nil {
		return err
	}
	store.noteRenumber(tx, mv.oldID, mv.newID)
	return nil
}

// subtreeMove is one move of a subtree to a sibling number.
type subtreeMove struct {
	oldID, newID string
	node         renumberTarget
	seq          int
}
