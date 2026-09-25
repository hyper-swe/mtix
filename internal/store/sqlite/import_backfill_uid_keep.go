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
// local node carries a marked backfill uid and has the same title, takes
// that uid, unless the file holds the uid on a node already. A task with
// another title is a different task (differentTitleNoUID): it gets a new
// backfill uid, so it never inherits the replaced task's uid, and a
// teammate who holds that task sees a different task under the id. A board
// written before uids were
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
		if l, ok := local[n.ID]; ok && n.UID == "" && !held[l.UID] && !differentTitleNoUID(l, n) {
			n.UID = l.UID
		}
	}
	return &kept, nil
}

// localBackfillUIDs returns, read through tx, the uid and title of every
// local node that carries a marked backfill uid, by id (MTIX-95.31.9).
func localBackfillUIDs(ctx context.Context, tx *sql.Tx) (nodes map[string]*exportNode, err error) {
	// Every local node's uid and title, soft-deleted nodes included.
	rows, err := tx.QueryContext(ctx, `SELECT id, uid, title FROM nodes WHERE uid IS NOT NULL AND uid <> ''`)
	if err != nil {
		return nil, fmt.Errorf("read the local uids a replace keeps: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil && err == nil {
			err = fmt.Errorf("close the local uids a replace keeps: %w", closeErr)
		}
	}()
	nodes = make(map[string]*exportNode)
	for rows.Next() {
		n := &exportNode{}
		if err := rows.Scan(&n.ID, &n.UID, &n.Title); err != nil {
			return nil, fmt.Errorf("read the local uids a replace keeps: %w", err)
		}
		if model.IsBackfillUID(n.UID) {
			nodes[n.ID] = n
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the local uids a replace keeps: %w", err)
	}
	return nodes, nil
}

// keptUIDCopy returns in, the file's copy of local node l, as a replace
// writes it (MTIX-95.31.9): with l's uid when in has none, l carries a
// marked backfill uid and the titles match, which the replace keeps
// (keepLocalBackfillUIDs), so the comparison does not count the uid as
// lost; otherwise in itself.
func keptUIDCopy(l, in *exportNode) *exportNode {
	if in.UID != "" || !model.IsBackfillUID(l.UID) || differentTitleNoUID(l, in) {
		return in
	}
	c := *in
	c.UID = l.UID
	return &c
}

// asTheMergeSeesIt returns local node l as a merge compares it
// (MTIX-95.31.9): a node without a uid with a new backfill uid, as the
// merge gives it before any identity decision (stampMissingUIDs), so the
// automatic import's refusal, the merge and its adoption report agree;
// otherwise l itself. l is never changed.
func asTheMergeSeesIt(l *exportNode) (*exportNode, error) {
	if l.UID != "" {
		return l, nil
	}
	uid, err := model.NewBackfillUID()
	if err != nil {
		return nil, fmt.Errorf("compare local node %s as the merge does: %w", l.ID, err)
	}
	c := *l
	c.UID = uid
	return &c, nil
}
