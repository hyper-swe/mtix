// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
// index cell) is skipped with a note, so each row is read once; a
// duplicated misread cell is handled once, so it writes one note
// (MTIX-107.80). So Export never holds two nodes with one id, and a mirror
// copy never reaches a row through another node's entry.
//
// Duplicate rows are chosen by nonempty uid, then content hash against an
// eligible mirror (MTIX-107.78.2). Equal matches retain the existing first-kept
// order. Every losing row is reported after the final winner is known.
func salvageRows(ctx context.Context, db *sql.DB, keys []salvageKey, out *dbSalvage, res *RecoverResult, mirror *mirrorSource) {
	st := newSalvageState(mirror)
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
		if st.ownEntry[k.rowid] {
			st.repeated = append(st.repeated, k)
			continue
		}
		if _, kept := st.keptAt[k.id]; kept {
			if chooseDuplicateRow(out, st, row) {
				st.ownEntry[k.rowid] = true
			}
			continue
		}
		keepSalvagedRow(out, st, res, row)
		st.ownEntry[k.rowid] = true
	}
	seen := map[salvageKey]bool{} // misread entries already handled
	for _, row := range misread {
		if seen[row.key] {
			continue // a duplicated misread cell: its note is already written (MTIX-107.80)
		}
		seen[row.key] = true
		keepMisreadRow(out, st, res, row)
	}
	finishDuplicateChoices(out, st, res)
}

// salvageState is what salvageRows knows about the rows it has handled
// (MTIX-107.78): the rowids kept through their own index entry, the rowid of
// the row kept as each id, losing duplicate rows, and deferred index notes.
type salvageState struct {
	ownEntry      map[int64]bool
	keptAt        map[string]int64
	reported      map[int64]bool
	mirror        *mirrorSource
	mirrors       map[string]*exportNode
	queued        map[int64]bool
	discarded     []salvagedRow
	discardedByID map[string][]int
	misreadNotes  []misreadNote
	firstMisread  map[int64]salvageKey
	repeated      []salvageKey
	choices       map[string]string
	columns       map[string][]*unreadableColumnError
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
func keepSalvagedRow(out *dbSalvage, st *salvageState, res *RecoverResult, row salvagedRow) {
	out.nodes[row.node.ID] = row.node
	if len(row.cols) > 0 {
		st.columns[row.node.ID] = row.cols
	}
	st.keptAt[row.node.ID] = row.key.rowid
	res.RecoveredIDs = append(res.RecoveredIDs, row.node.ID)
}

// keepMisreadRow registers an index entry for A that reaches a row carrying B.
// Same-row hits are reused. Different rows with B are compared against the
// mirror, and only the loser is queued; notes wait for the final selection.
func keepMisreadRow(out *dbSalvage, st *salvageState, res *RecoverResult, row salvagedRow) {
	a, b := row.key.id, row.node.ID
	res.LostIDs = append(res.LostIDs, a)
	if _, seen := st.firstMisread[row.key.rowid]; !seen {
		st.firstMisread[row.key.rowid] = row.key
	}
	st.misreadNotes = append(st.misreadNotes, misreadNote{key: row.key, nodeID: b})
	keptAt, already := st.keptAt[b]
	switch {
	case st.ownEntry[row.key.rowid], already && keptAt == row.key.rowid:
		return
	case already:
		chooseDuplicateRow(out, st, row)
	default:
		keepSalvagedRow(out, st, res, row)
	}
}

// reportDuplicateRow writes one note for a row that carries an id already
// kept from another row (MTIX-107.78): the table holds two rows with that
// id. The note names both rowids and holds the content of the row that is
// not salvaged, as exported JSON, and names its unreadable JSON columns, so
// nothing from it is dropped without a note. A row is reported once.
func reportDuplicateRow(st *salvageState, res *RecoverResult, row salvagedRow) {
	if st.reported[row.key.rowid] {
		return
	}
	st.reported[row.key.rowid] = true
	b := row.node.ID
	note := fmt.Sprintf("kept the row by %s; ", st.choices[b])
	note += fmt.Sprintf("node %s: the table holds two rows with id %s: the row at rowid %d is salvaged as %s, "+
		"and the row at rowid %d is not salvaged; ", b, b, st.keptAt[b], b, row.key.rowid)
	content, err := json.Marshal(row.node)
	if err != nil {
		note += fmt.Sprintf("its content could not be encoded: %v", err)
	} else {
		note += "its content is " + string(content)
	}
	for _, col := range row.cols {
		note += fmt.Sprintf("; its %s column could not be read: %v", col.column, col.cause)
	}
	res.Notes = append(res.Notes, note)
}
