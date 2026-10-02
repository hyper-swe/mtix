// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
)

// Re-addressing of pending events after a hub-rejection renumber
// (MTIX-95.37).
//
// INVARIANT: no event of a task whose creation the hub rejected as a
// renumber travels under the old number. The old number now belongs to a
// teammate's task, and a hub (or a peer) that applied the event by number
// would change that task. RenumberSubtree moves the nodes and emits no
// events, so every PENDING event of the moved subtree still names the old
// numbers: the node_id of its own events, the parent_id of its children's
// creations, and the target of any dependency link or unlink that points
// into it. requeueRenumberedEvents rewrites them to the new numbers in the
// renumber's own transaction. An event the hub already holds is history and
// is left as it is; push does not send an event about a task before the
// task's creation is on the hub (Store.EventsAwaitingCreation), so none
// exists for a task whose creation the hub rejected.

// requeueRenumberedEvents requeues the create event of the task with uid
// (now at newID, was oldID) and re-addresses the pending events of its
// subtree and the pending links into it, inside tx.
func requeueRenumberedEvents(ctx context.Context, tx *sql.Tx, uid, oldID, newID string) error {
	oldLike, newLike := escapeLIKEPrefix(oldID)+".%", escapeLIKEPrefix(newID)+".%"
	steps := []struct {
		what  string
		query string
		args  []any
	}{
		// Every pending event whose uid belongs to a node of the moved
		// subtree takes that node's current number. This covers the task's
		// own events and those of its descendants, children's creations
		// included.
		{"events by uid", `
			UPDATE sync_events
			   SET node_id = (SELECT n.id FROM nodes n WHERE n.uid = sync_events.uid AND n.uid <> '')
			 WHERE sync_status = 'pending' AND uid IS NOT NULL AND uid <> ''
			   AND uid IN (SELECT n.uid FROM nodes n
			                WHERE (n.id = ? OR n.id LIKE ? ESCAPE '\') AND n.uid <> '')`,
			[]any{newID, newLike}},
		// A pending event without a uid (queued before events carried one)
		// names its task by number only: swap the old prefix for the new.
		{"events by number", `
			UPDATE sync_events
			   SET node_id = ? || SUBSTR(node_id, ?)
			 WHERE sync_status = 'pending' AND (uid IS NULL OR uid = '')
			   AND (node_id = ? OR node_id LIKE ? ESCAPE '\')`,
			[]any{newID, len(oldID) + 1, oldID, oldLike}},
		// The creation of a child names its parent in its payload.
		{"child creations", `
			UPDATE sync_events
			   SET payload = json_set(payload, '$.parent_id',
			         ? || SUBSTR(json_extract(payload, '$.parent_id'), ?))
			 WHERE sync_status = 'pending' AND op_type = 'create_node'
			   AND node_id LIKE ? ESCAPE '\' AND json_valid(payload)
			   AND (json_extract(payload, '$.parent_id') = ?
			        OR json_extract(payload, '$.parent_id') LIKE ? ESCAPE '\')`,
			[]any{newID, len(oldID) + 1, newLike, oldID, oldLike}},
		// A link or unlink names the task it points to in its payload.
		{"links into the subtree", `
			UPDATE sync_events
			   SET payload = json_set(payload, '$.depends_on_node_id',
			         ? || SUBSTR(json_extract(payload, '$.depends_on_node_id'), ?))
			 WHERE sync_status = 'pending' AND op_type IN ('link_dep', 'unlink_dep')
			   AND json_valid(payload)
			   AND (json_extract(payload, '$.depends_on_node_id') = ?
			        OR json_extract(payload, '$.depends_on_node_id') LIKE ? ESCAPE '\')`,
			[]any{newID, len(oldID) + 1, oldID, oldLike}},
		// The rejected creation is sent again, under the new number.
		{"the rejected creation", `
			UPDATE sync_events SET node_id = ?, sync_status = 'pending'
			 WHERE event_id = ? AND op_type = 'create_node'`,
			[]any{newID, uid}},
	}
	for _, st := range steps {
		if _, err := tx.ExecContext(ctx, st.query, st.args...); err != nil {
			return fmt.Errorf("re-address %s: %w", st.what, err)
		}
	}
	return nil
}
