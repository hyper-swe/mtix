// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/format"
)

// A peer can apply an event only to a task whose creation it holds, so a
// merge on a store that syncs journals events only for a task the sync log
// holds the creation of (round 5, rule G). A task the merge created is never
// eligible: it is written here and named unsent (ImportResult.UnsentTasks),
// and it reaches the hub when its author pushes it. A held task whose
// creation the log lacks gets no event either, nor does a link to it.

// eligible reports whether the sync log can carry events for task id.
func (p *mergePlanner) eligible(id string) (bool, error) {
	if v, ok := p.elig[id]; ok {
		return v, nil
	}
	v, err := p.computeEligible(id)
	if err != nil {
		return false, err
	}
	if p.elig == nil {
		p.elig = map[string]bool{}
	}
	p.elig[id] = v
	return v, nil
}

func (p *mergePlanner) computeEligible(id string) (bool, error) {
	if p.newTasks[id] {
		return false, nil // the author's push sends a task the merge created
	}
	var uid string
	var deleted bool
	// The task's uid and whether it is deleted: a deleted task takes no event.
	err := p.tx.QueryRowContext(p.ctx, `SELECT COALESCE(uid, ''), deleted_at IS NOT NULL
		FROM nodes WHERE id = ?`, id).Scan(&uid, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read task %s: %w", id, err)
	}
	if before, held := p.j.before[id]; held {
		deleted = before.DeletedAt != "" // deleted by this merge: its delete event is still journaled
	}
	if deleted {
		return false, nil
	}
	return p.createInLog(id, uid)
}

// createInLog reports whether the sync log holds the creation of the task: a
// create_node row under its number or uid, or a create applied from the hub.
func (p *mergePlanner) createInLog(id, uid string) (bool, error) {
	var held bool
	// A create_node event for the task is journaled, or one under its uid was applied.
	if err := p.tx.QueryRowContext(p.ctx, `SELECT EXISTS (SELECT 1 FROM sync_events
		WHERE op_type = 'create_node' AND (node_id = ? OR (? <> '' AND ((uid = ? AND uid <> '') OR event_id = ?))))
		OR EXISTS (SELECT 1 FROM applied_events WHERE ? <> '' AND event_id = ?)`,
		id, uid, uid, uid, uid, uid).Scan(&held); err != nil {
		return false, fmt.Errorf("check the sync log for %s: %w", format.EventID(id), err)
	}
	return held, nil
}

// eligibleOrNote is eligible, naming the task as unsent when it is not.
func (p *mergePlanner) eligibleOrNote(id string) (bool, error) {
	ok, err := p.eligible(id)
	if err == nil && !ok {
		p.j.noteUnsent(id)
	}
	return ok, err
}

// depEligible reports whether both ends of a dependency can be carried; a live
// end that cannot is named as unsent. A deleted end that the log created still
// carries the link, as a peer applies it to a deleted task (it blocks).
func (p *mergePlanner) depEligible(d *exportDep) (bool, error) {
	from, err := p.depEndEligible(d.FromID)
	if err != nil {
		return false, err
	}
	to, err := p.depEndEligible(d.ToID)
	return from && to, err
}

func (p *mergePlanner) depEndEligible(id string) (bool, error) {
	var uid string
	var deleted bool
	// The end's uid and whether it is deleted: a deleted end is checked against the log alone.
	err := p.tx.QueryRowContext(p.ctx, `SELECT COALESCE(uid, ''), deleted_at IS NOT NULL FROM nodes WHERE id = ?`,
		id).Scan(&uid, &deleted)
	if err == nil && deleted && !p.newTasks[id] {
		ok, logErr := p.createInLog(id, uid)
		if logErr == nil && !ok {
			p.j.noteUnsent(id)
		}
		return ok, logErr
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return false, fmt.Errorf("read task %s: %w", id, err)
	}
	return p.eligibleOrNote(id)
}

// planChangedTasks plans every changed task the log can carry; one it cannot
// is named as unsent when the merge changed it.
func (p *mergePlanner) planChangedTasks() error {
	for _, id := range p.j.changed {
		after, err := p.storedNode(id)
		if err != nil {
			return err
		}
		before := p.j.before[id]
		ok, err := p.eligible(id)
		if err != nil {
			return err
		}
		if !ok {
			if len(nodeFieldDiffs(&before, &after)) > 0 {
				p.j.noteUnsent(id)
			}
			continue
		}
		if err := p.planChanged(&before, &after); err != nil {
			return err
		}
	}
	return nil
}
