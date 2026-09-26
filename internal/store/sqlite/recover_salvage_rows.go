// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// salvageKey is one primary-key index entry: the node id it indexes and the
// rowid of the row it points at (MTIX-95.31.3).
type salvageKey struct {
	id    string
	rowid int64
}

// salvageKeys walks the primary-key index, which often survives damage to
// table leaf pages. The index holds both the id and the rowid, so SQLite
// answers the walk from the index alone; each entry yields the id it
// indexes and the rowid it points at (MTIX-95.31.3).
func salvageKeys(ctx context.Context, db *sql.DB) ([]salvageKey, error) {
	rows, err := db.QueryContext(ctx, `SELECT id, rowid FROM nodes ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var keys []salvageKey
	for rows.Next() {
		var k salvageKey
		if err := rows.Scan(&k.id, &k.rowid); err != nil {
			return keys, nil //nolint:nilerr // keep what the index yielded before the error
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		return keys, nil //nolint:nilerr // partial index walk is still salvage
	}
	return keys, nil
}

// salvagedRow is a row read through a primary-key index entry: the entry,
// the node the row holds, and the JSON columns of it that could not be read
// (MTIX-95.31.3).
type salvagedRow struct {
	key  salvageKey
	node exportNode
	cols []*unreadableColumnError
}

// salvageRows reads, by rowid, the row each index entry points at, so the
// row's columns, its id included, come from the table, and keeps every row
// in out under the id it carries (MTIX-95.31.3). An entry whose row cannot
// be read lists its id in LostIDs.
//
// Under a damaged index an entry for id A can point at a row that carries
// another id, B. Such a row is set aside until every entry has been read,
// then handled by keepMisreadRow: nothing from it is kept under A, and it is
// exported once at most, as B. A itself ends up exactly one way: salvaged
// from the database when another entry reaches A's own row, supplied by the
// mirror merge when the mirror holds A, or listed as lost (A is added to
// LostIDs here, and Recover drops it from that list once A is salvaged). An
// entry that repeats the id and rowid of a row already kept (a duplicated
// index cell) is skipped with a note, so each row is read once. So Export
// never holds two nodes with one id, and a mirror copy never reaches a row
// through another node's entry.
func salvageRows(ctx context.Context, db *sql.DB, keys []salvageKey, out *dbSalvage, res *RecoverResult) {
	ownEntry := map[int64]bool{} // rowids kept through their own index entry
	var misread []salvagedRow
	for _, k := range keys {
		row, err := readSalvageRow(ctx, db, k)
		if err != nil {
			res.LostIDs = append(res.LostIDs, k.id)
			continue
		}
		if row.node.ID != k.id {
			misread = append(misread, row)
			continue
		}
		if ownEntry[k.rowid] {
			res.Notes = append(res.Notes, fmt.Sprintf(
				"node %s: a second index entry for %s points at the same row, which is salvaged once", k.id, k.id))
			continue
		}
		keepSalvagedRow(out, res, row)
		ownEntry[k.rowid] = true
	}
	for _, row := range misread {
		keepMisreadRow(out, res, row, ownEntry[row.key.rowid])
	}
}

// readSalvageRow reads the row index entry k points at, by rowid
// (MTIX-95.31.3). A row whose JSON column does not parse is still returned,
// without that column, and the column is listed in cols (MTIX-95.31.1).
func readSalvageRow(ctx context.Context, db *sql.DB, k salvageKey) (salvagedRow, error) {
	n, err := scanExportNode(db.QueryRowContext(ctx, exportNodeSelectSQL+" WHERE rowid = ?", k.rowid))
	cols := unreadableColumns(err)
	if len(cols) > 0 {
		err = nil
	}
	if err != nil {
		return salvagedRow{}, err
	}
	n.NodeType = string(model.NodeTypeForDepth(n.Depth))
	return salvagedRow{key: k, node: n, cols: cols}, nil
}

// keepSalvagedRow keeps row in out under the id it carries and lists it as
// recovered, with its unreadable JSON columns (MTIX-95.31.3).
func keepSalvagedRow(out *dbSalvage, res *RecoverResult, row salvagedRow) {
	out.nodes[row.node.ID] = row.node
	out.unreadable = append(out.unreadable, row.cols...)
	res.RecoveredIDs = append(res.RecoveredIDs, row.node.ID)
}

// keepMisreadRow handles a row that the index entry for id A reached
// although the row carries id B (MTIX-95.31.3). Nothing from the row is kept
// under A, and A is listed in LostIDs until Recover finds it salvaged
// another way (see salvageRows). The row is kept once, as B, unless it is
// already kept: through B's own entry (ownEntry), or as B through another
// entry. The note says only what is known: the entry for A points at the
// row of B, and what happened to that row.
func keepMisreadRow(out *dbSalvage, res *RecoverResult, row salvagedRow, ownEntry bool) {
	a, b := row.key.id, row.node.ID
	res.LostIDs = append(res.LostIDs, a)
	note := fmt.Sprintf("node %s: the index entry for %s points at the row of %s; ", a, a, b)
	_, already := out.nodes[b]
	switch {
	case ownEntry:
		note += fmt.Sprintf("the entry of %s reaches that row, so it is not salvaged again", b)
	case already:
		note += fmt.Sprintf("a row is already salvaged as %s, so this one is not salvaged again", b)
	default:
		keepSalvagedRow(out, res, row)
		note += fmt.Sprintf("no entry of %s reaches that row, so it is salvaged once, as %s", b, b)
	}
	res.Notes = append(res.Notes, note)
}
