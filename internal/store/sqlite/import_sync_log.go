// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// ErrReplaceOnSyncedStore marks a replace import refused because the store
// syncs with a hub (MTIX-107.31). The import wrote nothing.
var ErrReplaceOnSyncedStore = errors.New("replace import refused on a store that syncs with a hub")

// SyncLogRecovery names the commands that rebuild a synced store's board
// from the hub: unpushed changes go to the hub first, then the local board
// and sync log are dropped together and pulled again (MTIX-107.31).
const SyncLogRecovery = "mtix sync push, check that mtix sync status shows pending 0, then mtix sync reconcile --discard-local --yes, then mtix sync pull"

// maxNamedNodes caps how many node ids a refusal or a doctor check names.
const maxNamedNodes = 10

// rowQuerier is a *sql.DB or a *sql.Tx.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// HasSyncedWithHub reports whether the store syncs with a hub or relay
// (MTIX-107.31): it carries the hub-connected mark (MTIX-112.17.3), or holds
// an event it pulled, or one it pushed. A store whose events are all still
// pending, with no mark, does not.
func (s *Store) HasSyncedWithHub(ctx context.Context) (bool, error) {
	return syncedWithHub(ctx, s.writeDB)
}

func syncedWithHub(ctx context.Context, q rowQuerier) (bool, error) {
	var synced bool
	// The sticky mark, any pulled event, or any own event past pending
	// (pushed or conflicted).
	// A file relay peer shares events too, though its events stay pending in
	// the journal: one that published or ingested counts as synced. A publisher
	// published when its cursor is past zero, or its next relay sequence is past
	// one, or its epoch is past one: gap repair (RepublishRelayFrom) and
	// reset-peer (ResetRelayPublisher) rewind the cursor to a floor, and the
	// sequence and the epoch only ever advance on a publish and a reset. An
	// ingestor has a cursor row for a peer.
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM board_flags WHERE key = ?)
		OR EXISTS (SELECT 1 FROM applied_events)
		OR EXISTS (SELECT 1 FROM sync_events WHERE sync_status <> 'pending')
		OR EXISTS (SELECT 1 FROM relay_push_cursor WHERE cursor > 0 OR next_rs > 1 OR pub_epoch > 1)
		OR EXISTS (SELECT 1 FROM relay_ingest_cursor)`, hubMarkKey).Scan(&synced)
	if err != nil {
		return false, fmt.Errorf("read whether the store syncs with a hub: %w", err)
	}
	return synced, nil
}

// loggedNode is a local node the sync log holds an event for.
type loggedNode struct {
	id, uid, title string
	// pushed is true when the creation of the node is past pending: the hub
	// knows the task by its uid, so a pending-only carry (carryReplacedUIDs)
	// cannot move it.
	pushed bool
}

// loggedNodeMatch is the SQL condition tying sync event se to node n: by
// uid when the node has one (the event carries it, or it is the creation
// whose event id is the uid, ADR-003 section 2), and by number for an event
// without a uid.
const loggedNodeMatch = `((COALESCE(n.uid, '') <> '' AND (se.uid = n.uid
		OR (se.op_type = 'create_node' AND se.event_id = n.uid)))
	 OR ((se.uid IS NULL OR se.uid = '') AND se.node_id = n.id))`

// loggedNodes returns the local nodes the sync log holds an event for,
// read through q.
func loggedNodes(ctx context.Context, q rowQuerier) (nodes []loggedNode, err error) {
	// Each node with an event of the log, and whether its creation is past
	// pending. Matched by uid first, as the comparison of an
	// automatic import is (DiffReplace, MTIX-95.31.4).
	rows, err := q.QueryContext(ctx, `SELECT n.id, COALESCE(n.uid, ''), n.title,
		EXISTS (SELECT 1 FROM sync_events se WHERE `+loggedNodeMatch+`
			AND se.op_type = 'create_node' AND se.sync_status <> 'pending')
		FROM nodes n
		WHERE EXISTS (SELECT 1 FROM sync_events se WHERE `+loggedNodeMatch+`)
		ORDER BY n.id`)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		var n loggedNode
		if err := rows.Scan(&n.id, &n.uid, &n.title, &n.pushed); err != nil {
			return nil, err
		}
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// keepsLoggedNode reports whether the file data holds the task of local
// node l: by its uid under any number (a renumber is a move, not a loss,
// MTIX-95.31.4), or, for a node without a uid, by its number. A task that
// keeps its number and title but takes the file's uid has its pending
// events carried to it (carryReplacedUIDs, MTIX-95.31.16); that is not a
// loss unless the creation of the task was already pushed, except when the
// task's uid is a marked backfill uid: the hub never knew the task by it,
// and a board written before uids were shared adopts the file's uid.
func keepsLoggedNode(l loggedNode, byUID map[string]bool, byID map[string]*exportNode) bool {
	f := byID[l.id]
	if l.uid == "" {
		return f != nil
	}
	if byUID[l.uid] {
		return true
	}
	return f != nil && f.UID != "" && f.Title == l.title && (!l.pushed || model.IsBackfillUID(l.uid))
}

// refuseDroppingLoggedNodes returns an error wrapping
// ErrReplaceOnSyncedStore when the store has synced with a hub and the
// replace of data (the file as the replace writes it) would leave a local
// task whose events the sync log holds without its task (MTIX-107.31). A
// task is the same task by its uid: dropping it, or keeping its number
// while dropping or changing its uid, loses it. The pull would acknowledge
// the logged events as held (MTIX-95.2) instead of applying them, so the
// task would never come back. A renumbered task keeps its uid and is not
// refused. It runs in the import's transaction tx, before anything is
// deleted.
func refuseDroppingLoggedNodes(ctx context.Context, tx *sql.Tx, data *ExportData) error {
	synced, err := syncedWithHub(ctx, tx)
	if err != nil || !synced {
		return err
	}
	droppedNodes, err := droppedLoggedNodes(ctx, tx, data)
	if err != nil {
		return err
	}
	dropped := make([]string, 0, len(droppedNodes))
	for _, l := range droppedNodes {
		dropped = append(dropped, l.id)
	}
	if len(dropped) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the file does not hold %s as the same task (a task is matched by its uid), and "+
		"their events stay in the local sync log, so a pull cannot restore them; nothing was imported. "+
		"To rebuild the board from the hub, run %s: %w",
		ErrReplaceOnSyncedStore, nameNodes(dropped), SyncLogRecovery, model.ErrInvalidInput)
}

// droppedLoggedNodes returns the local nodes the sync log holds an event for
// that the replace of data would lose (keepsLoggedNode), read through tx.
func droppedLoggedNodes(ctx context.Context, tx *sql.Tx, data *ExportData) ([]loggedNode, error) {
	byUID := make(map[string]bool, len(data.Nodes))
	byID := make(map[string]*exportNode, len(data.Nodes))
	for i := range data.Nodes {
		n := &data.Nodes[i]
		byID[n.ID] = n
		if n.UID != "" {
			byUID[n.UID] = true
		}
	}
	logged, err := loggedNodes(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("read the nodes the sync log holds: %w", err)
	}
	var dropped []loggedNode
	for _, l := range logged {
		if !keepsLoggedNode(l, byUID, byID) {
			dropped = append(dropped, l)
		}
	}
	return dropped, nil
}

// discardUnsharedEvents deletes, on a store that never synced, the pending
// events of every task the replace of data drops, the held copies of those
// events, and the pending link events that point at the dropped tasks
// (MTIX-107.31.3). A store that never synced has shared nothing (every event
// is pending, no relay peer published or ingested), so the events of a task
// the new board lacks would otherwise reach a hub or relay with the first
// push, creating a task this board does not hold. A synced store is refused
// before this runs (refuseDroppingLoggedNodes); it does nothing for one.
func discardUnsharedEvents(ctx context.Context, tx *sql.Tx, data *ExportData) error {
	synced, err := syncedWithHub(ctx, tx)
	if err != nil || synced {
		return err
	}
	dropped, err := droppedLoggedNodes(ctx, tx, data)
	if err != nil {
		return err
	}
	for _, l := range dropped {
		// The pending events of the dropped task, and of the links to it:
		// first the held copies, then the events themselves.
		for _, stmt := range []string{deleteHeldCopiesOfDropped, deleteEventsOfDropped} {
			if _, err := tx.ExecContext(ctx, stmt, l.id); err != nil {
				return fmt.Errorf("discard the unshared events of %s: %w", l.id, err)
			}
		}
	}
	return nil
}

// droppedEventIDs selects the ids of the pending events of the node bound as
// ?1 (matched by uid first, loggedNodeMatch) and of the pending link events
// that name it as their target.
const droppedEventIDs = `SELECT se.event_id FROM sync_events se
	WHERE se.sync_status = 'pending' AND (
	  EXISTS (SELECT 1 FROM nodes n WHERE n.id = ?1 AND ` + loggedNodeMatch + `)
	  OR (se.op_type IN ('link_dep', 'unlink_dep') AND json_valid(se.payload)
	      AND json_extract(se.payload, '$.depends_on_node_id') = ?1))`

// deleteHeldCopiesOfDropped removes the push-hold copies (sync_quarantine)
// of the events droppedEventIDs selects.
const deleteHeldCopiesOfDropped = `DELETE FROM sync_quarantine WHERE source = 'push' AND event_id IN (` + droppedEventIDs + `)`

// deleteEventsOfDropped removes the pending events droppedEventIDs selects.
const deleteEventsOfDropped = `DELETE FROM sync_events WHERE sync_status = 'pending' AND event_id IN (` + droppedEventIDs + `)`

// MissingLoggedNodes returns the ids of the nodes the local sync log
// creates that the board lacks, with no delete event for them: projection
// drift a pull cannot repair, because it acknowledges the held events
// instead of applying them (MTIX-107.31, ADR-006 section 5.3). A creation is
// matched by its uid first (the uid of the event, or its event id), so a
// renumbered task is not reported and a task whose uid was replaced under
// the same number is; a creation without a uid falls back to the number.
func (s *Store) MissingLoggedNodes(ctx context.Context) ([]string, error) {
	// create_node events whose task is not on the board by uid (by number
	// when the event carries none), and that no delete event of the same
	// node follows (gc purges deleted nodes).
	ids, err := queryIDs(ctx, s.writeDB, `SELECT DISTINCT se.node_id FROM sync_events se
		WHERE se.op_type = 'create_node'
		  AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.uid <> '' AND n.uid = COALESCE(NULLIF(se.uid, ''), se.event_id))
		  AND (COALESCE(se.uid, '') <> '' OR NOT EXISTS (SELECT 1 FROM nodes n WHERE n.id = se.node_id))
		  AND NOT EXISTS (SELECT 1 FROM sync_events d WHERE d.node_id = se.node_id AND d.op_type = 'delete')
		ORDER BY se.node_id`)
	if err != nil {
		return nil, fmt.Errorf("read the nodes the sync log creates: %w", err)
	}
	return ids, nil
}

// queryIDs returns the single text column of query's rows.
func queryIDs(ctx context.Context, q rowQuerier, query string) (ids []string, err error) {
	rows, err := q.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// NameNodes renders ids for a message: up to maxNamedNodes of them, then
// how many more there are.
func NameNodes(ids []string) string {
	return nameNodes(ids)
}

func nameNodes(ids []string) string {
	if len(ids) <= maxNamedNodes {
		return strings.Join(ids, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(ids[:maxNamedNodes], ", "), len(ids)-maxNamedNodes)
}
