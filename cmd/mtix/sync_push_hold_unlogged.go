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
func releaseLoggedHolds(ctx context.Context, store *sqlite.Store, holds []sqlite.HeldPushEvent, p *holdPlan) error {
	var ids []string
	for _, h := range holds {
		if strings.HasPrefix(h.Reason, holdUnloggedPrefix) {
			ids = append(ids, h.EventID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	states, err := readUnloggedHoldStates(ctx, store, ids)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if states.releasable(id) {
			p.released[id] = holdLoggedWhy
		}
	}
	return nil
}
