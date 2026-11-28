// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

// The SQL of the staged import plan (MTIX-2.3.2.2, FR-7.8). Every statement is
// a constant, one per lookup: no statement is built from a fragment, a column
// or a table name (CLAUDE.md, SQL Rule 1), and every value is bound. Each one
// that reads or writes a plan table is served by an index or a key, or is the
// single pass its comment says; TestPlanQueries_UseAnIndex pins the query plan
// of every statement in this file (SQL Rule 1b).
//
// The plan tables are in planSchemaSQL (import_plan.go). The indexes are not
// partial: SQLite uses a partial index only for a query that repeats its
// predicate, and a bound equality does not (the same lesson as MTIX-95.47).

const (
	// A page of staged nodes in file order, from the primary key (kind, ord).
	stageNodePageSQL = `SELECT ord, body FROM stage_rows WHERE kind = ? AND ord > ? ORDER BY ord LIMIT ?`

	// One staged node by its place in the file, from the primary key.
	stageNodeAtSQL = `SELECT body FROM stage_rows WHERE kind = ? AND ord = ?`

	// One entry of a report list, numbered after the entries before it.
	planAddRowSQL = `INSERT INTO plan_rows (sec, ord, c1, c2, c3, c4, c5, n) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`

	// A number the plan took under a parent (a primary key insert).
	planTakeSQL = `INSERT OR IGNORE INTO plan_taken (k, seq) VALUES (?, ?)`

	// Whether the plan took this number under this parent (primary key).
	planTakenSQL = `SELECT 1 FROM plan_taken WHERE k = ? AND seq = ?`

	// The identity of one incoming node, and the id, uid, parent and number the
	// reconcile will leave it with (the same until a rewrite).
	planInsertIncomingSQL = `INSERT INTO plan_in (ord, id, uid, parent_id, project, depth, seq, created_at, title, prov,
		f_id, f_uid, f_parent, f_seq) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	// The incoming node at a place in the file (rowid).
	planIncomingByOrdSQL = `SELECT ord, id, uid, parent_id, project, depth, seq, created_at, title, f_id, f_uid, f_parent,
		f_seq FROM plan_in WHERE ord = ? ORDER BY ord DESC LIMIT 1`

	// The last incoming node of an id, as the in-memory plan's maps keep it
	// (index plan_in_id).
	planIncomingByIDSQL = `SELECT ord, id, uid, parent_id, project, depth, seq, created_at, title, f_id, f_uid, f_parent,
		f_seq FROM plan_in WHERE id = ? ORDER BY ord DESC LIMIT 1`

	// The last incoming node that ends with a uid (index plan_in_fuid).
	planIncomingByFUIDSQL = `SELECT ord, id, uid, parent_id, project, depth, seq, created_at, title, f_id, f_uid, f_parent,
		f_seq FROM plan_in WHERE f_uid = ? ORDER BY ord DESC LIMIT 1`

	// For each uid held by more than one node: the second node and the first.
	// One pass over plan_in; both uid lookups are searches of plan_in_uid.
	planDuplicateUIDsSQL = `INSERT INTO plan_rows (sec, ord, c1, c2, c3, c4, c5, n)
		SELECT ?1, ROW_NUMBER() OVER (ORDER BY b.ord) - 1 + ?2, b.uid, b.id, a.id, '', '', ?3
		FROM plan_in b JOIN plan_in a ON a.ord = (SELECT MIN(ord) FROM plan_in WHERE uid = b.uid)
		WHERE b.uid <> '' AND b.ord = (SELECT MIN(ord) FROM plan_in WHERE uid = b.uid AND ord > a.ord)`

	// The incoming node takes a fresh uid (rowid).
	planRestampSQL = `UPDATE plan_in SET f_uid = ? WHERE ord = ?`

	// The provisional nodes, shallowest first, then by id and place. One pass
	// over plan_in.
	planProvOrderSQL = `INSERT INTO plan_order (pos, ord)
		SELECT ROW_NUMBER() OVER (ORDER BY depth, id, ord) - 1, ord FROM plan_in WHERE prov = 1`

	// How many provisional nodes there are. One pass over plan_order.
	planProvCountSQL = `SELECT COUNT(*) FROM plan_order`

	// The node renumbered at a position (rowid).
	planProvAtSQL = `SELECT ord FROM plan_order WHERE pos = ?`

	// The node's new id, parent and number (rowid); descendants follow through
	// plan_rebase.
	planRenumberInSQL = `UPDATE plan_in SET f_id = ?, f_parent = ?, f_seq = ? WHERE ord = ?`

	// A descendant follows a renumbered node to its new path (primary key).
	planRebaseSetSQL = `INSERT OR REPLACE INTO plan_rebase (old, new) VALUES (?, ?)`

	// The new path of the renumbered ancestor holding a prefix (primary key).
	planRebaseGetSQL = `SELECT new FROM plan_rebase WHERE old = ?`

	// Where to start looking for a free number under a parent (primary key).
	planHintGetSQL = `SELECT next FROM plan_hint WHERE k = ?`

	// Every number below this one is unavailable under the parent (primary key).
	planHintSetSQL = `INSERT OR REPLACE INTO plan_hint (k, next) VALUES (?, ?)`

	// Every node a merge may move: deleted rows still own their ids. One pass
	// over the store's nodes.
	planLoadLocalSQL = `SELECT id, COALESCE(uid, ''), project, COALESCE(parent_id, ''), seq, COALESCE(created_at, ''), title
		FROM nodes ORDER BY depth, id`

	// One local node, ending where it is until the plan moves it.
	planInsertLocalSQL = `INSERT INTO plan_local (pos, id, uid0, uid, project, parent_id, seq, created_at, title, stamp,
		final, final_seq, renumbered) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, 0)`

	// The local plan row at a position (rowid).
	planLocalByPosSQL = `SELECT pos, id, uid0, uid, project, parent_id, seq, created_at, title, stamp, final, final_seq,
		renumbered FROM plan_local WHERE pos = ? LIMIT 1`

	// The local plan row of an id (index plan_local_id).
	planLocalByIDSQL = `SELECT pos, id, uid0, uid, project, parent_id, seq, created_at, title, stamp, final, final_seq,
		renumbered FROM plan_local WHERE id = ? LIMIT 1`

	// The local plan row that ends at an id (index plan_local_final).
	planLocalByFinalSQL = `SELECT pos, id, uid0, uid, project, parent_id, seq, created_at, title, stamp, final, final_seq,
		renumbered FROM plan_local WHERE final = ? LIMIT 1`

	// The next local node after a position that still has no uid: the walk
	// moves forward from the last one it stamped, so it never rescans.
	planStampNextSQL = `SELECT pos, id FROM plan_local WHERE uid = '' AND pos > ? ORDER BY pos LIMIT 1`

	// The minted uid, marked as the import's (rowid).
	planStampSetSQL = `UPDATE plan_local SET uid = ?, stamp = 1 WHERE pos = ?`

	// The id, number and renumbered mark the plan gave the node (rowid).
	planSaveFinalSQL = `UPDATE plan_local SET final = ?, final_seq = ?, renumbered = ? WHERE pos = ?`

	// The highest number a local node holds, now or after its move, under a
	// parent: the nodes whose parent ends at the parent, or is the parent and
	// not local (indexes plan_local_final and plan_local_parent).
	planLocalMaxSQL = `SELECT COALESCE(MAX(MAX(seq, final_seq)), 0) FROM plan_local
		WHERE project = ?1 AND (parent_id IN (SELECT id FROM plan_local WHERE final = ?2)
		  OR (parent_id = ?2 AND NOT EXISTS (SELECT 1 FROM plan_local WHERE id = ?2)))`

	// The highest number a file node holds under a parent (index plan_in_parent,
	// which ends in seq, so the maximum is read from the index).
	planFileMaxSQL = `SELECT COALESCE(MAX(seq), 0) FROM plan_in WHERE project = ? AND parent_id = ?`

	// Whether a local node is the id or under it (ids from "id." up to "id/").
	planNamespaceLocalSQL = `SELECT 1 FROM plan_local WHERE id = ?1 OR (id >= ?1 || '.' AND id < ?1 || '/') LIMIT 1`

	// Whether a file node is the id or under it (ids from "id." up to "id/").
	planNamespaceInSQL = `SELECT 1 FROM plan_in WHERE id = ?1 OR (id >= ?1 || '.' AND id < ?1 || '/') LIMIT 1`

	// The first local node, in plan order, that ends where an earlier one does:
	// one pass over plan_local, each match a search of plan_local_final.
	planCheckFinalsSQL = `SELECT a.id, b.id, b.final FROM plan_local b
		JOIN plan_local a ON a.final = b.final AND a.pos < b.pos ORDER BY b.pos, a.pos LIMIT 1`

	// The store's node of an id, with the columns of an export node (primary key).
	planLocalNodeSQL = exportNodeSelectSQL + ` WHERE id = ?`

	// Every workflow conflict is given the choice (the primary key prefix).
	planChoiceAllSQL = `UPDATE plan_rows SET c2 = ?1 WHERE sec = ?2`

	// The last workflow conflict of a task is given the choice, as the in-memory
	// plan keys its conflicts by task id and the last one wins (index plan_rows_c3).
	planChoiceTaskSQL = `UPDATE plan_rows SET c2 = ?1 WHERE sec = ?2 AND c3 = ?3
		AND ord = (SELECT MAX(ord) FROM plan_rows WHERE sec = ?2 AND c3 = ?3)`

	// The conflicts still without a choice, and those counted as no-ops (the
	// primary key prefix).
	planCountOpenSQL = `SELECT COUNT(*) FILTER (WHERE c2 = ''), COUNT(*) FILTER (WHERE n = 1)
		FROM plan_rows WHERE sec = ?`

	// A page of one report list, in order (primary key).
	planPageSQL = `SELECT c1, c2, c3, c4, c5, n FROM plan_rows WHERE sec = ? AND ord > ? ORDER BY ord LIMIT ?`

	// The remap pairs are rebuilt from the rows; the old ones go first.
	planRemapClearSQL = `DELETE FROM plan_remap`

	// The remap pairs of a list of renumbered nodes (primary key prefix). A
	// minted uid is listed only once the import is applied (MTIX-95.31.9).
	planRemapFillSQL = `INSERT INTO plan_remap (uid, path)
		SELECT c1, c3 FROM plan_rows WHERE sec = ? AND (? OR n = 0) ORDER BY ord`

	// The local uids a merge gave up map to the id their task keeps
	// (MTIX-95.31.6; primary key prefix).
	planRemapAdoptSQL = `INSERT INTO plan_remap (uid, path)
		SELECT c2, c1 FROM plan_rows WHERE sec = ? AND c2 <> '' ORDER BY ord`

	// One pair per uid after a uid, the last entry of a uid winning, in uid
	// order (index plan_remap_uid).
	planRemapPageSQL = `SELECT uid, path, MAX(k) FROM plan_remap
		WHERE uid <> '' AND uid > ? GROUP BY uid ORDER BY uid LIMIT ?`
)
