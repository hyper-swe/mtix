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

// localRenumber is a local task that a merge import moves out of the way
// (MTIX-95.31.4, FR-15.2i): the file holds a different task (another uid)
// under its id, so the local task and its subtree move to number seq under
// the same parent (newID), keeping their uids, and the file's task takes
// the id.
type localRenumber struct {
	uid, oldID, newID string
	seq               int
}

// localNodeAt is what a merge needs to know about the local node that holds
// an id, live or soft-deleted.
type localNodeAt struct {
	uid, project, parentID string
	seq                    int
}

// differentTask reports whether the local node and the file's node under
// the same id are different tasks: both carry a uid and the uids differ
// (MTIX-95.31.4). A node without a uid (an export older than uids, or a
// store not yet backfilled) has no identity to compare.
func differentTask(local, in *exportNode) bool {
	return local.UID != "" && in.UID != "" && local.UID != in.UID
}

// refuseDifferentTask returns ErrConflict when a merge would overwrite the
// local node with a different task that holds its id in the file
// (MTIX-95.31.4). ImportReconcile renumbers such a local task first, so a
// merge it runs never reaches this; a direct merge (Store.Import) or a store
// that changed after the plan fails here and writes nothing.
func refuseDifferentTask(local, in *exportNode) error {
	if !differentTask(local, in) {
		return nil
	}
	return fmt.Errorf("merge node %s: the file holds a different task under this id (uid %s, local uid %s); "+
		"mtix import renumbers the local task first: %w", in.ID, in.UID, local.UID, model.ErrConflict)
}

// planLocalRenumbers plans how a merge import keeps every local task that
// holds the id of a different task in the file (MTIX-95.31.4, FR-15.2i,
// ADR-003 §6). A replace keeps no local task, so only a merge plans moves.
// The file's nodes are visited shallowest first; each one whose id the
// store gives to a different task (differentTask) moves that local task and
// its subtree to the next number free under its parent in both the store
// and the file (nextSeqFreeInBoth), so the published board keeps its
// numbers. A node under a local task already planned to move no longer
// meets it. Every moved node, the descendants included, is added to
// report.LocalRenumbers (uid, old id, new id). Nothing is written.
func (s *Store) planLocalRenumbers(
	ctx context.Context, data *ExportData, mode ImportMode,
	report *ImportReconcileReport, taken map[string]map[int]bool,
) ([]localRenumber, error) {
	if mode != ImportModeMerge {
		return nil, nil
	}
	var moves []localRenumber
	for _, idx := range shallowestFirst(data, func(*exportNode) bool { return true }) {
		n := &data.Nodes[idx]
		if n.UID == "" || underMoved(moves, n.ID) {
			continue
		}
		local, found, err := readLocalNodeAt(ctx, s.readDB, n.ID)
		if err != nil {
			return nil, err
		}
		if !found || local.uid == "" || local.uid == n.UID {
			continue // no local node, or the same task (n.UID is set): merge updates it
		}
		seq, err := s.nextSeqFreeInBoth(ctx, &local, data, taken)
		if err != nil {
			return nil, err
		}
		move := localRenumber{uid: local.uid, oldID: n.ID, newID: model.BuildID(local.project, local.parentID, seq), seq: seq}
		moves = append(moves, move)
		if err := s.reportLocalMove(ctx, move, report); err != nil {
			return nil, err
		}
	}
	return moves, nil
}

// underMoved reports whether id is a planned move's old id or lies under it.
func underMoved(moves []localRenumber, id string) bool {
	for _, m := range moves {
		if id == m.oldID || strings.HasPrefix(id, m.oldID+".") {
			return true
		}
	}
	return false
}

// readLocalNodeAt reads, through q, the uid, project, parent and number of
// the node (live or soft-deleted) that holds id, and whether one does.
func readLocalNodeAt(ctx context.Context, q queryable, id string) (localNodeAt, bool, error) {
	var n localNodeAt
	// The node holding the id, soft-deleted rows included: they still own it.
	err := q.QueryRowContext(ctx,
		`SELECT COALESCE(uid, ''), project, COALESCE(parent_id, ''), seq FROM nodes WHERE id = ?`,
		id).Scan(&n.uid, &n.project, &n.parentID, &n.seq)
	if errors.Is(err, sql.ErrNoRows) {
		return n, false, nil
	}
	if err != nil {
		return n, false, fmt.Errorf("read local node %s: %w", id, err)
	}
	return n, true, nil
}

// nextSeqFreeInBoth returns the number a local task moved by a merge takes
// (MTIX-95.31.4): the first number after the highest one the store (live
// or soft-deleted nodes) and the file hold under the task's parent, that no
// earlier move in this plan took and whose namespace (the id itself, or an
// id under it) is free in the store and in the file. Starting after the
// highest number never gives the task a number another task held. The
// number is recorded in taken, which the provisional renumbering of the
// same import also reads.
func (s *Store) nextSeqFreeInBoth(
	ctx context.Context, local *localNodeAt, data *ExportData, taken map[string]map[int]bool,
) (int, error) {
	key := local.parentID
	if key == "" {
		key = "\x00" + local.project // roots: one namespace per project
	}
	if taken[key] == nil {
		taken[key] = make(map[int]bool)
	}
	highest, err := s.highestChildSeq(ctx, local.project, local.parentID)
	if err != nil {
		return 0, err
	}
	for i := range data.Nodes {
		if n := &data.Nodes[i]; n.Project == local.project && n.ParentID == local.parentID && n.Seq > highest {
			highest = n.Seq
		}
	}
	for seq := highest + 1; ; seq++ {
		candidate := model.BuildID(local.project, local.parentID, seq)
		if taken[key][seq] || fileNamespaceHeld(data, candidate) {
			continue
		}
		held, err := s.storeNamespaceHeld(ctx, candidate)
		if err != nil {
			return 0, err
		}
		if !held {
			taken[key][seq] = true
			return seq, nil
		}
	}
}

// highestChildSeq returns the highest number a node of project holds under
// parentID (a root when empty) in the store, soft-deleted nodes included,
// or 0.
func (s *Store) highestChildSeq(ctx context.Context, project, parentID string) (int, error) {
	var highest int
	// Highest sibling number under the parent, deleted rows included.
	err := s.readDB.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM nodes WHERE project = ? AND COALESCE(parent_id, '') = ?`,
		project, parentID).Scan(&highest)
	if err != nil {
		return 0, fmt.Errorf("read the highest number under %q: %w", parentID, err)
	}
	return highest, nil
}

// storeNamespaceHeld reports whether a store node (live or soft-deleted)
// holds id or an id under it.
func (s *Store) storeNamespaceHeld(ctx context.Context, id string) (bool, error) {
	var one int
	// Any node at id or below it; LIKE is escaped so '_' and '%' match themselves.
	err := s.readDB.QueryRowContext(ctx,
		`SELECT 1 FROM nodes WHERE id = ? OR id LIKE ? ESCAPE '\' LIMIT 1`,
		id, escapeLIKEPrefix(id)+".%").Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check node id %s: %w", id, err)
	}
	return true, nil
}

// fileNamespaceHeld reports whether a node of the file holds id or an id
// under it.
func fileNamespaceHeld(data *ExportData, id string) bool {
	for i := range data.Nodes {
		if nid := data.Nodes[i].ID; nid == id || strings.HasPrefix(nid, id+".") {
			return true
		}
	}
	return false
}

// reportLocalMove adds the planned move to report.LocalRenumbers, followed
// by each local descendant (live or soft-deleted) that moves with it.
func (s *Store) reportLocalMove(ctx context.Context, move localRenumber, report *ImportReconcileReport) error {
	report.LocalRenumbers = append(report.LocalRenumbers,
		ImportRemapEntry{UID: move.uid, OldPath: move.oldID, NewPath: move.newID})
	// Every node under the moved task, in id order.
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id, COALESCE(uid, '') FROM nodes WHERE id LIKE ? ESCAPE '\' ORDER BY id`,
		escapeLIKEPrefix(move.oldID)+".%")
	if err != nil {
		return fmt.Errorf("list the subtree of %s: %w", move.oldID, err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close subtree rows", "error", closeErr)
		}
	}()
	for rows.Next() {
		var id, uid string
		if err := rows.Scan(&id, &uid); err != nil {
			return fmt.Errorf("list the subtree of %s: %w", move.oldID, err)
		}
		report.LocalRenumbers = append(report.LocalRenumbers,
			ImportRemapEntry{UID: uid, OldPath: id, NewPath: move.newID + id[len(move.oldID):]})
	}
	return rows.Err()
}

// applyLocalRenumbers moves each planned local task and its subtree inside
// the import's transaction tx, before the file's nodes are written
// (MTIX-95.31.4), through the renumber path RenumberSubtree uses
// (renumberSubtreeTx). A task that no longer holds the planned id and uid
// under the planned parent (the store changed after the plan) fails with
// ErrConflict, and the transaction writes nothing. The sequence counters
// follow the moved numbers when Import rebuilds them after the
// transaction.
func applyLocalRenumbers(ctx context.Context, tx *sql.Tx, moves []localRenumber) error {
	for _, m := range moves {
		local, found, err := readLocalNodeAt(ctx, tx, m.oldID)
		if err != nil {
			return err
		}
		if !found || local.uid != m.uid || model.BuildID(local.project, local.parentID, m.seq) != m.newID {
			return fmt.Errorf("renumber local task %s: it changed after the import was planned; run the import again: %w",
				m.oldID, model.ErrConflict)
		}
		target := renumberTarget{project: local.project, parentID: local.parentID, seq: local.seq}
		if err := renumberSubtreeTx(ctx, tx, m.oldID, target, m.seq); err != nil {
			return fmt.Errorf("renumber local task %s to %s: %w", m.oldID, m.newID, err)
		}
	}
	return nil
}
