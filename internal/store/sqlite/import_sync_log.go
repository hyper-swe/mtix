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
const SyncLogRecovery = "mtix sync push, then mtix sync reconcile --discard-local --yes, then mtix sync pull"

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
	err := q.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM board_flags WHERE key = ?)
		OR EXISTS (SELECT 1 FROM applied_events)
		OR EXISTS (SELECT 1 FROM sync_events WHERE sync_status <> 'pending')`, hubMarkKey).Scan(&synced)
	if err != nil {
		return false, fmt.Errorf("read whether the store syncs with a hub: %w", err)
	}
	return synced, nil
}

// refuseDroppingLoggedNodes returns an error wrapping
// ErrReplaceOnSyncedStore when the store has synced with a hub and the
// replace of data would delete a local node whose events the sync log
// holds (MTIX-107.31). The pull would acknowledge those events as held
// (MTIX-95.2) instead of applying them, so the node would never come back.
// It runs in the import's transaction tx, before anything is deleted.
func refuseDroppingLoggedNodes(ctx context.Context, tx *sql.Tx, data *ExportData) error {
	synced, err := syncedWithHub(ctx, tx)
	if err != nil || !synced {
		return err
	}
	inFile := make(map[string]bool, len(data.Nodes))
	for i := range data.Nodes {
		inFile[data.Nodes[i].ID] = true
	}
	// Local nodes the sync log holds an event for, by id.
	logged, err := queryIDs(ctx, tx, `SELECT DISTINCT n.id FROM nodes n
		JOIN sync_events se ON se.node_id = n.id ORDER BY n.id`)
	if err != nil {
		return fmt.Errorf("read the nodes the sync log holds: %w", err)
	}
	var dropped []string
	for _, id := range logged {
		if !inFile[id] {
			dropped = append(dropped, id)
		}
	}
	if len(dropped) == 0 {
		return nil
	}
	return fmt.Errorf("%w: the file lacks %s, whose events stay in the local sync log, so a pull cannot "+
		"restore them; nothing was imported. To rebuild the board from the hub, run %s: %w",
		ErrReplaceOnSyncedStore, nameNodes(dropped), SyncLogRecovery, model.ErrInvalidInput)
}

// MissingLoggedNodes returns the ids of the nodes the local sync log
// creates that the board lacks, with no delete event for them: projection
// drift a pull cannot repair, because it acknowledges the held events
// instead of applying them (MTIX-107.31, ADR-006 section 5.3). A node is
// matched by id or by uid, so a renumbered node is not reported.
func (s *Store) MissingLoggedNodes(ctx context.Context) ([]string, error) {
	// create_node events whose node is on the board neither by id nor by
	// uid, and that no delete event of the same node follows (gc purges
	// deleted nodes).
	ids, err := queryIDs(ctx, s.writeDB, `SELECT DISTINCT se.node_id FROM sync_events se
		WHERE se.op_type = 'create_node'
		  AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.id = se.node_id)
		  AND NOT EXISTS (SELECT 1 FROM nodes n WHERE n.uid <> '' AND n.uid = COALESCE(NULLIF(se.uid, ''), se.event_id))
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
