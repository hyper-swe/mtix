// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// replaceAllData drops all data and reimports from export per FR-7.8,
// inside the import's transaction. A node of the file without a uid, at an
// id whose local node carries a marked backfill uid, keeps that uid
// (keepLocalBackfillUIDs, MTIX-95.31.9).
func replaceAllData(ctx context.Context, tx *sql.Tx, data *ExportData) (ImportResult, error) {
	kept, err := keepLocalBackfillUIDs(ctx, tx, data)
	if err != nil {
		return ImportResult{}, err
	}
	if err := clearAllTables(ctx, tx); err != nil {
		return ImportResult{}, err
	}
	return insertAllExportData(ctx, tx, kept)
}

// keepLocalBackfillUIDs returns the file a replace writes (MTIX-95.31.9):
// data, or a copy of it in which every node without a uid, at an id whose
// local node carries a marked backfill uid, takes that uid, unless the
// file holds the uid on a node already. A board written before uids were
// shared then never changes the uid a store gave such a task: the uid
// stays stable, the automatic import does not count it as lost
// (keptUIDCopy), and a later pull of such a board imports as before. A
// create-time uid (a UUIDv7) is not kept: the file wins, and the comparison
// counts it as lost (MTIX-95.31.4). data itself is never changed.
func keepLocalBackfillUIDs(ctx context.Context, tx *sql.Tx, data *ExportData) (*ExportData, error) {
	local, err := localBackfillUIDs(ctx, tx)
	if err != nil || len(local) == 0 {
		return data, err
	}
	held := make(map[string]bool, len(data.Nodes))
	for i := range data.Nodes {
		held[data.Nodes[i].UID] = true
	}
	kept := *data
	kept.Nodes = append([]exportNode(nil), data.Nodes...)
	for i := range kept.Nodes {
		n := &kept.Nodes[i]
		if uid, ok := local[n.ID]; ok && n.UID == "" && !held[uid] {
			n.UID = uid
		}
	}
	return &kept, nil
}

// localBackfillUIDs returns, read through tx, the marked backfill uid of
// every local node that carries one, by id (MTIX-95.31.9).
func localBackfillUIDs(ctx context.Context, tx *sql.Tx) (uids map[string]string, err error) {
	// Every local node's uid, soft-deleted nodes included.
	rows, err := tx.QueryContext(ctx, `SELECT id, uid FROM nodes WHERE uid IS NOT NULL AND uid <> ''`)
	if err != nil {
		return nil, fmt.Errorf("read the local uids a replace keeps: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close the local uids a replace keeps: %w", closeErr)
		}
	}()
	uids = make(map[string]string)
	for rows.Next() {
		var id, uid string
		if err := rows.Scan(&id, &uid); err != nil {
			return nil, fmt.Errorf("read the local uids a replace keeps: %w", err)
		}
		if model.IsBackfillUID(uid) {
			uids[id] = uid
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the local uids a replace keeps: %w", err)
	}
	return uids, nil
}

// keptUIDCopy returns in, the file's copy of local node l, as a replace
// writes it (MTIX-95.31.9): with l's uid when in has none and l carries a
// marked backfill uid, which the replace keeps (keepLocalBackfillUIDs), so
// the comparison does not count the uid as lost; otherwise in itself.
func keptUIDCopy(l, in *exportNode) *exportNode {
	if in.UID != "" || !model.IsBackfillUID(l.UID) {
		return in
	}
	c := *in
	c.UID = l.UID
	return &c
}
