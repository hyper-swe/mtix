// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// historyTracker follows one write transaction: the tasks whose recorded
// fields were read before they could change (before) and the ids already read.
// A task that did not exist then has no snapshot, so everything it holds
// afterwards is new.
type historyTracker struct {
	before  map[string]historySnapshot
	seen    map[string]bool
	origins map[string]model.HistoryOrigin // tasks reached by an automatic cascade
	moves   []idMove                       // renumbers in this transaction
	renames map[string]string              // new id -> old id, for a bulk rename (ImportAs)
}

// idMove is a renumber: oldRoot and its descendants became newRoot and the
// same descendants under it, so an after-id pairs with a before-id by suffix.
type idMove struct{ oldRoot, newRoot string }

// oldOf returns the id a task had before the transaction's renumbers, if one
// of them moved it.
func (t *historyTracker) oldOf(id string) (string, bool) {
	for _, m := range t.moves {
		if id == m.newRoot || strings.HasPrefix(id, m.newRoot+".") {
			return m.oldRoot + id[len(m.newRoot):], true
		}
	}
	return "", false
}

// noteRenumber tells the transaction's tracker that oldRoot's subtree is now
// newRoot's, before the rewrite, so the move is recorded as one change per task.
func (s *Store) noteRenumber(tx *sql.Tx, oldRoot, newRoot string) {
	if s == nil {
		return
	}
	if v, ok := s.history.Load(tx); ok {
		tr := v.(*historyTracker) //nolint:errcheck,forcetypeassert // only withHistoryTx stores a *historyTracker
		tr.moves = append(tr.moves, idMove{oldRoot: oldRoot, newRoot: newRoot})
	}
}

// originOf returns the origin of an automatic change to task id, if any.
func (t *historyTracker) originOf(id string) (model.HistoryOrigin, bool) {
	o, ok := t.origins[id]
	return o, ok
}

// historyToucher lets a write helper that has no Store (autoUnblockNode, the
// sync applier) ask for a task to be tracked before it writes it. *Store
// implements it, and a nil *Store is a no-op, so a caller outside a recording
// transaction is unaffected.
type historyToucher interface {
	touchHistory(ctx context.Context, tx *sql.Tx, id string) error
}

// touchHistory reads the recorded fields of task id into the transaction's
// tracker, once, before the caller writes the task. Outside a withHistoryTx
// transaction, or on a nil Store, it does nothing. MTIX-95.31.19.3.
func (s *Store) touchHistory(ctx context.Context, tx *sql.Tx, id string) error {
	if s == nil || id == "" {
		return nil
	}
	v, ok := s.history.Load(tx)
	if !ok {
		return nil
	}
	tr := v.(*historyTracker) //nolint:errcheck,forcetypeassert // only withHistoryTx stores a *historyTracker
	if tr.seen[id] {
		return nil
	}
	tr.seen[id] = true
	tr.origins[id] = model.HistoryOriginSystem // not the transaction's own task: reached by a cascade
	return readSnapshots(ctx, tx, tr.before, historySnapshotSQL, id)
}

// replayTarget returns the id of the task a replayed event is about: the live
// task holding the event's uid, else the task its number names. When neither
// exists yet (a create), it is the number the event carries.
func replayTarget(ctx context.Context, tx *sql.Tx, e *model.SyncEvent) string {
	if id, err := resolveNodeRef(ctx, tx, e); err == nil {
		return id
	}
	return e.NodeID
}

// noteRenames tells the transaction's tracker how a bulk rename moved ids
// (old id -> new id), before the rewrite, so each renamed task pairs with its
// earlier state even when it has no uid.
func (s *Store) noteRenames(tx *sql.Tx, mapping map[string]string) {
	v, ok := s.history.Load(tx)
	if !ok {
		return
	}
	tr := v.(*historyTracker) //nolint:errcheck,forcetypeassert // only withHistoryTx stores a *historyTracker
	tr.renames = make(map[string]string, len(mapping))
	for oldID, newID := range mapping {
		tr.renames[newID] = oldID
	}
}
