// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/hyper-swe/mtix/internal/model"
)

// localRenumber is one local node whose own number a merge import changes
// (MTIX-95.31.4, FR-15.2i): the node holding uid, now at oldID, ends at
// newID, number seq, under the same parent (whose id may change too).
// stamp: the node has no uid yet and is given uid first (MTIX-95.31.9).
type localRenumber struct {
	uid, oldID, newID string
	seq               int
	stamp             bool
}

// localNode is what a merge import plans with for one local node, live or
// soft-deleted (MTIX-95.31.4).
type localNode struct {
	id, uid, project, parentID, createdAt string
	seq, finalSeq                         int
	// title is read only when a uid is missing (MTIX-95.31.9); stamp is
	// set when the plan minted uid for the node (stampMissingUIDs).
	title string
	stamp bool
	// final is the node's id after the merge.
	final string
	// renumbered is true when the node, or an ancestor, moves off an id the
	// file gives to a different task.
	renumbered bool
}

// taskIdentity is what tells two tasks under one id apart: the uid and the
// creation time (MTIX-95.31.4).
type taskIdentity struct{ uid, createdAt string }

// differentTask reports whether the local node and the file's node under
// the same id are different tasks (MTIX-95.31.4, differentIdentity).
func differentTask(local, in *exportNode) bool {
	return differentIdentity(taskIdentity{local.UID, local.CreatedAt}, taskIdentity{in.UID, in.CreatedAt})
}

// differentIdentity reports whether a and b, which hold the same id, are
// different tasks (MTIX-95.31.4). Calling two tasks different is safe (a
// merge renumbers one, only with confirmation); calling two tasks the same
// loses one. So nodes that both carry a uid, with different uids, are one
// task only when their creation times are equal and at least one uid is a
// UUIDv7 minted more than an hour after that time, or a marked backfill uid
// (backfilledLater): a uid a clone assigned when it upgraded from before
// uids were shared, or to a task it imported without one (MTIX-95.31.9). A
// merge then adopts the file's uid. Every other
// pair is two tasks: a uid minted before created_at (a node a hub applied
// records the apply time), within the hour after it (a create that waited
// for the write lock), or not a UUIDv7, and creation times that differ or
// are missing. A node without a uid has no identity to compare.
func differentIdentity(a, b taskIdentity) bool {
	if a.uid == "" || b.uid == "" || a.uid == b.uid {
		return false
	}
	sameCreate := a.createdAt != "" && b.createdAt != "" && sameInstant(a.createdAt, b.createdAt)
	return !sameCreate || (!backfilledLater(a.uid, a.createdAt) && !backfilledLater(b.uid, b.createdAt))
}

// backfillAge is how long after a task's creation its uid must have been
// minted to count as a backfill (MTIX-95.31.4): far longer than any wait
// between reading the clock and minting the uid at creation.
const backfillAge = time.Hour

// backfilledLater reports whether uid was assigned after the task was
// created: a marked backfill uid, whatever the time (model.IsBackfillUID,
// MTIX-95.31.9), or a UUIDv7 whose embedded time is more than backfillAge
// after createdAt (MTIX-95.31.4). Any other uid, or a UUIDv7 with a
// creation time that cannot be read, is not.
func backfilledLater(uid, createdAt string) bool {
	if model.IsBackfillUID(uid) {
		return true
	}
	u, err := uuid.Parse(uid)
	if err != nil || u.Version() != 7 {
		return false
	}
	created, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return false
	}
	minted := time.UnixMilli(int64(binary.BigEndian.Uint64(u[:8]) >> 16)) //nolint:gosec // 48-bit ms field
	return minted.After(created.Add(backfillAge))
}

// sameInstant reports whether two RFC 3339 times are the same instant, or,
// when either cannot be read, the same text.
func sameInstant(a, b string) bool {
	ta, errA := time.Parse(time.RFC3339Nano, a)
	tb, errB := time.Parse(time.RFC3339Nano, b)
	if errA != nil || errB != nil {
		return a == b
	}
	return ta.Equal(tb)
}

// refuseDifferentTask returns ErrConflict when a merge would overwrite the
// local node with a different task that holds its id in the file
// (MTIX-95.31.4). ImportReconcile renumbers such a local task first, so a
// merge it runs never reaches this; a direct merge (Store.Import) or a store
// that changed after the plan fails here and writes nothing.
func refuseDifferentTask(local, in *exportNode) error {
	if err := refuseTitleMismatch(local, in); err != nil { // MTIX-95.31.9
		return err
	}
	if !differentTask(local, in) {
		return nil
	}
	return fmt.Errorf("merge node %s: the file holds a different task under this id (uid %s, local uid %s); "+
		"mtix import renumbers the local task first: %w", in.ID, in.UID, local.UID, model.ErrConflict)
}

// localMovePlan is the state of planning a merge's local moves.
type localMovePlan struct {
	data      *ExportData
	nodes     []*localNode // shallowest first, then by id
	byID      map[string]*localNode
	fileByID  map[string]*exportNode
	fileByUID map[string]*exportNode
	taken     map[string]map[int]bool
	report    *ImportReconcileReport
}

// planLocalRenumbers plans where a merge import puts the local tasks the
// file numbers differently (MTIX-95.31.4, FR-15.2i, ADR-003 §6). A replace
// keeps no local task, so only a merge plans moves. Nothing is written.
//
//  1. Moves: a local task whose uid the file holds under another id under
//     the same parent is the same task, renumbered by another clone: it
//     follows the file to that id, with its subtree, without confirmation
//     (report.Moved). One the file holds under another parent is a
//     ConflictLocalUIDMismatch in report.Conflicts.
//  2. Renumbers: after the moves, a local task at an id the file gives to a
//     different task (differentIdentity) is never overwritten: it and its
//     subtree move to the next number free under its parent in both the
//     store and the file (nextSeqFreeInBoth), keeping their uids, so the
//     published board keeps its numbers (report.LocalRenumbers, applied
//     only with confirmation). When either task has no uid to compare, the
//     titles decide (differentTaskAt, MTIX-95.31.9): such a pair with
//     different titles is renumbered and listed in report.TitleMismatches,
//     and a renumbered local task without a uid is given one.
//
// It returns the nodes whose own number changes, shallowest first; the
// numbers it takes are recorded in taken for the provisional renumbering.
func (s *Store) planLocalRenumbers(
	ctx context.Context, data *ExportData, mode ImportMode,
	report *ImportReconcileReport, taken map[string]map[int]bool,
) ([]localRenumber, error) {
	if mode != ImportModeMerge {
		return nil, nil
	}
	nodes, err := s.loadLocalNodes(ctx)
	if err != nil {
		return nil, err
	}
	p := newLocalMovePlan(data, nodes, report, taken)
	if err := s.loadTitlesIfUIDless(ctx, p); err != nil {
		return nil, err
	}
	p.follow()
	p.renumber()
	if err := p.checkFinals(); err != nil {
		return nil, err
	}
	if err := p.stampMissingUIDs(); err != nil {
		return nil, err
	}
	return p.finish(), nil
}

// loadLocalNodes reads every local node, soft-deleted ones included,
// shallowest first and then by id.
func (s *Store) loadLocalNodes(ctx context.Context) ([]*localNode, error) {
	// Every node a merge may move: deleted rows still own their ids.
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT id, COALESCE(uid, ''), project, COALESCE(parent_id, ''), seq,
		        COALESCE(created_at, '')
		   FROM nodes ORDER BY depth, id`)
	if err != nil {
		return nil, fmt.Errorf("read the local nodes for the merge plan: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			s.logger.Error("failed to close merge plan rows", "error", closeErr)
		}
	}()
	var nodes []*localNode
	for rows.Next() {
		n := &localNode{}
		if err := rows.Scan(&n.id, &n.uid, &n.project, &n.parentID, &n.seq, &n.createdAt); err != nil {
			return nil, fmt.Errorf("read the local nodes for the merge plan: %w", err)
		}
		n.finalSeq, n.final = n.seq, n.id
		nodes = append(nodes, n)
	}
	return nodes, rows.Err()
}

// newLocalMovePlan indexes the local nodes and the file's nodes.
func newLocalMovePlan(data *ExportData, nodes []*localNode, report *ImportReconcileReport,
	taken map[string]map[int]bool) *localMovePlan {
	p := &localMovePlan{data: data, nodes: nodes, taken: taken, report: report,
		byID:      make(map[string]*localNode, len(nodes)),
		fileByID:  make(map[string]*exportNode, len(data.Nodes)),
		fileByUID: make(map[string]*exportNode, len(data.Nodes)),
	}
	for _, n := range nodes {
		p.byID[n.id] = n
	}
	for i := range data.Nodes {
		f := &data.Nodes[i]
		p.fileByID[f.ID] = f
		if f.UID != "" {
			p.fileByUID[f.UID] = f
		}
	}
	return p
}

// finalOf returns the id local node id ends at, or id when it is not local.
func (p *localMovePlan) finalOf(id string) string {
	if n := p.byID[id]; n != nil {
		return n.final
	}
	return id
}

// place sets l's final id: under its parent's final id with its final
// number, or, while its number is unchanged, its id with the parent's part
// replaced (so a node whose parent is gone keeps its id).
func (p *localMovePlan) place(l *localNode) {
	parent := p.finalOf(l.parentID)
	switch {
	case l.finalSeq != l.seq:
		l.final = model.BuildID(l.project, parent, l.finalSeq)
	case l.parentID != "" && parent != l.parentID && strings.HasPrefix(l.id, l.parentID+"."):
		l.final = parent + l.id[len(l.parentID):]
	default:
		l.final = l.id
	}
}

// follow places every local node, shallowest first, under its parent's
// final id, and moves one whose uid the file holds under another id there
// (step 1 of planLocalRenumbers).
func (p *localMovePlan) follow() {
	for _, l := range p.nodes {
		parent := p.finalOf(l.parentID)
		p.place(l)
		f := p.fileByUID[l.uid]
		if l.uid == "" || f == nil || f.ID == l.final {
			continue
		}
		if f.ParentID != parent || model.BuildID(l.project, parent, f.Seq) != f.ID {
			p.report.Conflicts = append(p.report.Conflicts, ImportUIDConflict{
				UID: l.uid, ImportPath: f.ID, LocalPath: l.id, Kind: ConflictLocalUIDMismatch})
			continue
		}
		l.finalSeq, l.final = f.Seq, f.ID
		p.take(l.project, parent, f.Seq)
	}
}

// renumber moves every local node, shallowest first, off an id the file
// gives to a different task, to the next number free in both (step 2 of
// planLocalRenumbers). A node under a renumbered one moves with it.
func (p *localMovePlan) renumber() {
	for _, l := range p.nodes {
		parent := p.finalOf(l.parentID)
		if up := p.byID[l.parentID]; up != nil && up.renumbered {
			l.renumbered = true
		}
		p.place(l)
		f := p.fileByID[l.final]
		if f == nil || !p.differentTaskAt(l, f) {
			continue
		}
		p.noteTitleMismatch(l, f)
		l.finalSeq = p.nextSeqFreeInBoth(l.project, parent)
		l.final, l.renumbered = model.BuildID(l.project, parent, l.finalSeq), true
	}
}

// take records seq as taken under parent (a root when empty) of project.
func (p *localMovePlan) take(project, parent string, seq int) {
	key := takenKey(project, parent)
	if p.taken[key] == nil {
		p.taken[key] = make(map[int]bool)
	}
	p.taken[key][seq] = true
}

// takenKey keys taken numbers by parent id, and roots by project.
func takenKey(project, parent string) string {
	if parent == "" {
		return "\x00" + project // roots: one namespace per project
	}
	return parent
}

// nextSeqFreeInBoth returns the number a renumbered local task takes under
// parent (MTIX-95.31.4): the first number after the highest one held there
// by a local node (live or soft-deleted, before or after the planned moves
// and renumbers) or by a file node, whose namespace (the id itself, or an
// id under it) no local node and no file node holds. Starting after the
// highest number never gives the task a number another task held.
func (p *localMovePlan) nextSeqFreeInBoth(project, parent string) int {
	highest := 0
	for _, l := range p.nodes {
		if l.project == project && p.finalOf(l.parentID) == parent {
			highest = max(highest, l.seq, l.finalSeq)
		}
	}
	for i := range p.data.Nodes {
		if f := &p.data.Nodes[i]; f.Project == project && f.ParentID == parent {
			highest = max(highest, f.Seq)
		}
	}
	for seq := highest + 1; ; seq++ {
		if !p.namespaceHeld(model.BuildID(project, parent, seq)) {
			p.take(project, parent, seq)
			return seq
		}
	}
}

// namespaceHeld reports whether a local node or a file node holds id or an
// id under it (a node whose parent is gone keeps its id).
func (p *localMovePlan) namespaceHeld(id string) bool {
	under := func(other string) bool { return other == id || strings.HasPrefix(other, id+".") }
	for _, l := range p.nodes {
		if under(l.id) {
			return true
		}
	}
	for i := range p.data.Nodes {
		if under(p.data.Nodes[i].ID) {
			return true
		}
	}
	return false
}

// checkFinals returns ErrConflict when two local nodes would end at one id:
// a task moved to an id that a local task the merge keeps also holds.
func (p *localMovePlan) checkFinals() error {
	at := make(map[string]string, len(p.nodes))
	for _, l := range p.nodes {
		if other, held := at[l.final]; held {
			return fmt.Errorf("merge: local nodes %s and %s would both end at %s: %w",
				other, l.id, l.final, model.ErrConflict)
		}
		at[l.final] = l.id
	}
	return nil
}

// finish reports every local node whose id changes, shallowest first, as
// moved or renumbered (a node under a renumbered one is renumbered), and
// returns the nodes whose own number changes.
func (p *localMovePlan) finish() []localRenumber {
	var moves []localRenumber
	for _, l := range p.nodes {
		if l.final == l.id {
			continue
		}
		entry := ImportRemapEntry{UID: l.uid, OldPath: l.id, NewPath: l.final, NewUID: l.stamp}
		if l.renumbered {
			p.report.LocalRenumbers = append(p.report.LocalRenumbers, entry)
		} else {
			p.report.Moved = append(p.report.Moved, entry)
		}
		if l.finalSeq != l.seq {
			moves = append(moves, localRenumber{uid: l.uid, oldID: l.id, newID: l.final, seq: l.finalSeq, stamp: l.stamp})
		}
	}
	return moves
}

// applyLocalRenumbers moves the planned local nodes inside the import's
// transaction tx, before the file's nodes are written (MTIX-95.31.4),
// through the renumber path RenumberSubtree uses (renumberSubtreeTx). Every
// planned node must still hold its uid at its planned id; otherwise the
// store changed after the plan, the import fails with ErrConflict and the
// transaction writes nothing. The nodes move in two passes, parents first,
// each found by its uid: first to a temporary number above every number in
// use, then to its final number, which must give the planned id. So moves
// that trade ids (two tasks the file swapped) never meet an occupied id.
// The sequence counters follow the final numbers when Import rebuilds them
// after the transaction.
func applyLocalRenumbers(ctx context.Context, tx *sql.Tx, moves []localRenumber) error {
	if len(moves) == 0 {
		return nil
	}
	if err := stampNewUIDs(ctx, tx, moves); err != nil { // MTIX-95.31.9
		return err
	}
	for _, m := range moves {
		id, err := idOfUID(ctx, tx, m.uid)
		if err != nil {
			return err
		}
		if id != m.oldID {
			return fmt.Errorf("renumber local task %s: it is now %s; the store changed after the import "+
				"was planned, run the import again: %w", m.oldID, id, model.ErrConflict)
		}
	}
	base, err := temporarySeqBase(ctx, tx, moves)
	if err != nil {
		return err
	}
	for i, m := range moves {
		if err := moveByUID(ctx, tx, m.uid, base+i, ""); err != nil {
			return err
		}
	}
	for _, m := range moves {
		if err := moveByUID(ctx, tx, m.uid, m.seq, m.newID); err != nil {
			return err
		}
	}
	return nil
}

// idOfUID returns the id of the one node (live or soft-deleted) that holds
// uid; none, or more than one, is ErrConflict.
func idOfUID(ctx context.Context, tx *sql.Tx, uid string) (string, error) {
	var count int
	var id string
	// How many nodes hold uid, and one of their ids.
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MIN(id), '') FROM nodes WHERE uid = ?`, uid,
	).Scan(&count, &id); err != nil {
		return "", fmt.Errorf("find the node with uid %s: %w", uid, err)
	}
	if count != 1 {
		return "", fmt.Errorf("renumber the local task with uid %s: %d nodes hold it; the store changed after "+
			"the import was planned, run the import again: %w", uid, count, model.ErrConflict)
	}
	return id, nil
}

// temporarySeqBase returns a number above every number the store and the
// moves use: the first temporary number applyLocalRenumbers moves nodes to.
func temporarySeqBase(ctx context.Context, tx *sql.Tx, moves []localRenumber) (int, error) {
	var highest int
	// The highest number any node holds.
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM nodes`).Scan(&highest); err != nil {
		return 0, fmt.Errorf("read the highest node number: %w", err)
	}
	for _, m := range moves {
		highest = max(highest, m.seq)
	}
	return highest + 1, nil
}

// moveByUID moves the node holding uid, with its subtree, to number seq
// under its current parent. When want is set, the node's new id must be
// want, or the store changed after the plan (ErrConflict).
func moveByUID(ctx context.Context, tx *sql.Tx, uid string, seq int, want string) error {
	id, err := idOfUID(ctx, tx, uid)
	if err != nil {
		return err
	}
	var target renumberTarget
	// The node's project, parent and number, deleted rows included.
	if err := tx.QueryRowContext(ctx,
		`SELECT project, COALESCE(parent_id, ''), seq FROM nodes WHERE id = ?`, id,
	).Scan(&target.project, &target.parentID, &target.seq); err != nil {
		return fmt.Errorf("renumber local task %s: %w", id, err)
	}
	if newID := model.BuildID(target.project, target.parentID, seq); want != "" && newID != want {
		return fmt.Errorf("renumber local task %s: it would land at %s, not the planned %s; the store changed "+
			"after the import was planned, run the import again: %w", id, newID, want, model.ErrConflict)
	}
	if err := renumberSubtreeTx(ctx, tx, id, target, seq); err != nil {
		return fmt.Errorf("renumber local task %s to number %d: %w", id, seq, err)
	}
	return nil
}
