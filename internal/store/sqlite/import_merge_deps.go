// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// A merge on a store that syncs changes no dependency on a task the sync log
// holds (principle H, round 6). A dependency decides a status (a blocks link
// blocks its dependent), and the merge and a peer would reach it by two
// routes, so the merge leaves the link to the person: it is named with the
// mtix dep add command that makes it, which journals the link and lets the
// shared rule block the dependent everywhere.

// settleDep reports whether the merge inserts the file's dependency d. A store
// that does not sync (nil journal) inserts every one. On a store that syncs,
// one the store holds already is a no-op, one with an end the sync log holds is
// named and not applied, and one between tasks the log lacks is kept as the
// file wrote it, naming both ends unsent (no event carries them).
func (j *mergeJournal) settleDep(ctx context.Context, tx *sql.Tx, d *exportDep) (bool, error) {
	if j == nil {
		return true, nil
	}
	var held int
	// The merge inserts a dependency with INSERT OR IGNORE: one the store holds is not a change.
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE from_id = ? AND to_id = ? AND dep_type = ?`,
		d.FromID, d.ToID, d.DepType).Scan(&held); err != nil {
		return false, fmt.Errorf("check dependency %s->%s: %w", d.FromID, d.ToID, err)
	}
	if held > 0 || !model.DepType(d.DepType).IsValid() {
		return true, nil // an invalid type fails at the insert, as before
	}
	p := j.planner(ctx, tx)
	from, fromHeld, err := p.endInLog(d.FromID)
	if err != nil {
		return false, err
	}
	to, toHeld, err := p.endInLog(d.ToID)
	if err != nil {
		return false, err
	}
	if !from || !to {
		return true, nil // an end that is no task fails at the insert, as before
	}
	if fromHeld || toHeld {
		j.skippedDeps = append(j.skippedDeps, fmt.Sprintf("mtix dep add %s %s --type %s", d.FromID, d.ToID, d.DepType))
		return false, nil
	}
	j.noteUnsent(d.FromID)
	j.noteUnsent(d.ToID)
	return true, nil
}

// endInLog reads one end of a dependency: whether the store holds the task, and
// whether the sync log holds its creation. A task this merge created never is
// held by the log.
func (p *mergePlanner) endInLog(id string) (exists, inLog bool, err error) {
	var uid string
	// The end's uid; a missing row is no task.
	err = p.tx.QueryRowContext(p.ctx, `SELECT COALESCE(uid, '') FROM nodes WHERE id = ?`, id).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("read task %s: %w", id, err)
	}
	if p.newTasks[id] {
		return true, false, nil
	}
	inLog, err = p.createInLog(id, uid)
	return true, inLog, err
}
