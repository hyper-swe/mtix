// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// What became of the push holds of tasks the sync log has no creation for
// (MTIX-112.17.16.37, review r1 F1). Such a hold is not permanent: the
// task's creation can reach the log later, above all by pull, when a
// teammate pushed the creation of the same task (this clone merge-imported
// the teammate's board before it first synced). Every push checks each such
// hold again at its start and releases the ones whose task now has a
// logged creation, so the held edits are sent by the ordinary push. A hold
// of a task deleted here before its creation reached the log is retired:
// the delete, and while the task stays deleted or purged every other held
// event of it, stays as a record and is never sent, even once a pull logs
// the creation (reviews r1b S1 and r1c, lead rulings: a local cleanup
// never deletes a teammate's live task on the peers). A delete made after
// the creation is logged is ordinary and is sent; an undeleted task
// releases its held edits. sqlite.UnloggedSubjects lists a retired hold, as
// retired, so it is never released.

// holdLoggedWhy is why a hold of an unlogged task is released.
const holdLoggedWhy = "the sync log now holds the creation of its task"

// unloggedHoldStates is what the store says of a set of held events of
// unlogged tasks: the unlogged task each still depends on, and those whose
// task or link target is gone from the board.
type unloggedHoldStates struct {
	subjects map[string]sqlite.UnloggedSubject
	gone     map[string]bool
}

// readUnloggedHoldStates reads the unloggedHoldStates of the held events
// ids.
func readUnloggedHoldStates(ctx context.Context, st *sqlite.Store, ids []string) (unloggedHoldStates, error) {
	subjects, err := st.UnloggedSubjects(ctx, ids)
	if err != nil {
		return unloggedHoldStates{}, fmt.Errorf("check held events of unlogged tasks: %w", err)
	}
	gone, err := st.SubjectsGone(ctx, ids)
	if err != nil {
		return unloggedHoldStates{}, fmt.Errorf("check held events of unlogged tasks: %w", err)
	}
	return unloggedHoldStates{subjects: subjects, gone: gone}, nil
}

// releasable reports whether the hold of id ends: the event depends on no
// unlogged task any more, is not retired (a retired hold stays listed by
// UnloggedSubjects) and its task is still on the board.
func (s unloggedHoldStates) releasable(id string) bool {
	_, still := s.subjects[id]
	return !still && !s.gone[id]
}

// releaseLoggedHolds adds to p the release of every hold among holds of an
// unlogged task whose task now has a logged creation (releasable). It runs
// at the start of every push (releasePushHolds), before any event is read.
// Before the releases apply, it holds as retired the pending deletes that
// the released holds alone kept retired (holdPredatingDeletes), and returns
// those holds.
func releaseLoggedHolds(ctx context.Context, store *sqlite.Store, holds []sqlite.HeldPushEvent, p *holdPlan,
) ([]sqlite.QuarantinedEvent, error) {
	var ids []string
	for _, h := range holds {
		if strings.HasPrefix(h.Reason, holdUnloggedPrefix) {
			ids = append(ids, h.EventID)
		}
	}
	if len(ids) == 0 {
		return nil, nil
	}
	states, err := readUnloggedHoldStates(ctx, store, ids)
	if err != nil {
		return nil, err
	}
	var released []string
	for _, id := range ids {
		if states.releasable(id) {
			p.released[id] = holdLoggedWhy
			released = append(released, id)
		}
	}
	return holdPredatingDeletes(ctx, store, released)
}

// holdPredatingDeletes holds, retired, every pending delete of a task the
// released holds name that arrived before the task's creation was logged
// (sqlite.Store.PredatingDeletesNamedBy; lead ruling r2 2). Once those holds
// are released nothing names the task any more, so such a delete, made
// before a pull brought a teammate's creation of the task and undone here,
// would otherwise be sent and delete the teammate's task on the peers. It
// returns the holds it recorded.
func holdPredatingDeletes(ctx context.Context, store *sqlite.Store, released []string,
) ([]sqlite.QuarantinedEvent, error) {
	ids, err := store.PredatingDeletesNamedBy(ctx, released)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	events, err := store.ReadPendingEventsByID(ctx, ids)
	if err != nil {
		return nil, err
	}
	holds := make([]sqlite.QuarantinedEvent, 0, len(events))
	for _, e := range events {
		q, err := pushHold(e, unloggedHoldReason(e, sqlite.UnloggedSubject{NodeID: e.NodeID, UID: e.UID, Retired: true}))
		if err != nil {
			return nil, err
		}
		holds = append(holds, q)
	}
	if err := store.HoldPushEvents(ctx, holds, version); err != nil {
		return nil, err
	}
	return holds, nil
}
