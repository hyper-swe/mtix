// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// A merge leaves each dependent in the status a peer computes from the events
// the merge journals (principle A). The rules are the peer's own: a link_dep
// blocks the dependent (autoBlockNode); a transition to done, cancelled or
// invalidated releases the dependents whose blockers are all resolved
// (unblockDependents, decided by unblockTarget). Nothing here copies them.

// settleBlocks applies the peer rules to what the merge stored, in the order a
// peer meets them: the links block, a task that was blocked and still has a
// blocker is blocked again, and a blocker the merge resolved releases its
// dependents.
func (p *mergePlanner) settleBlocks() error {
	if err := p.autoBlockMerged(); err != nil {
		return err
	}
	if err := p.reblockMerged(); err != nil {
		return err
	}
	return p.unblockResolved()
}

// autoBlockMerged blocks, as a peer does on the link_dep event, the
// dependent of every blocks dependency the merge inserted (FR-3.8), and
// records the status each had before so no transition is journaled for it.
// A link the log cannot carry is never sent, so a peer never blocks the
// dependent: the block stays here and the dependent's status is named local only.
func (p *mergePlanner) autoBlockMerged() error {
	for i := range p.j.deps {
		d := p.j.deps[i]
		if d.DepType != string(model.DepTypeBlocks) {
			continue
		}
		if err := p.blockDependentOf(&d); err != nil {
			return err
		}
	}
	return nil
}

// blockDependentOf blocks the dependent of one inserted blocks dependency and
// records what it was, or names the block local only when the link stays here.
func (p *mergePlanner) blockDependentOf(d *exportDep) error {
	j := p.j
	var was string
	// The dependent's status before the block, for a live task only.
	if err := p.tx.QueryRowContext(p.ctx, `SELECT status FROM nodes WHERE id = ? AND deleted_at IS NULL`, d.ToID).Scan(&was); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("read status of %s: %w", d.ToID, err)
	}
	if err := autoBlockNode(p.ctx, p.tx, p.s, d.ToID, p.s.clock); err != nil {
		return fmt.Errorf("auto-block %s for the imported dependency: %w", d.ToID, err)
	}
	if was != string(model.StatusOpen) && was != string(model.StatusInProgress) {
		return nil
	}
	if j.blocked == nil {
		j.blocked = map[string]string{}
	}
	if _, held := j.blocked[d.ToID]; !held {
		j.blocked[d.ToID] = was
	}
	sent, err := p.depEligible(d)
	if err != nil {
		return err
	}
	if !sent && !p.newTasks[d.ToID] {
		j.noteLocalOnly(d.ToID, "status (blocked by a link that stays local)")
	}
	return nil
}

// reblockMerged keeps blocked a task that was blocked before the merge and
// still has a blocks dependency, when the merge wrote another status over it
// (a file chosen with --prefer theirs). One rule decides block and reblock,
// the peer's: any blocks dependency blocks an open or in-progress task,
// whatever state the blocker is in. A blocker the merge resolves releases it
// again in unblockResolved, as the journaled transition does on a peer.
func (p *mergePlanner) reblockMerged() error {
	for _, id := range p.j.changed {
		if p.j.before[id].Status != string(model.StatusBlocked) {
			continue
		}
		var status string
		// The merged task's status, for a live task only.
		if err := p.tx.QueryRowContext(p.ctx, `SELECT status FROM nodes WHERE id = ? AND deleted_at IS NULL`, id).Scan(&status); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return fmt.Errorf("read status of %s: %w", id, err)
		}
		if status != string(model.StatusOpen) && status != string(model.StatusInProgress) {
			continue
		}
		var blockers bool
		// Any blocks dependency on the task blocks it, whatever state the blocker is in.
		if err := p.tx.QueryRowContext(p.ctx, `SELECT EXISTS (SELECT 1 FROM dependencies
			WHERE to_id = ? AND dep_type = 'blocks')`, id).Scan(&blockers); err != nil {
			return fmt.Errorf("check blockers of %s: %w", id, err)
		}
		if !blockers {
			continue
		}
		if err := autoBlockNode(p.ctx, p.tx, p.s, id, p.s.clock); err != nil {
			return fmt.Errorf("keep %s blocked: %w", id, err)
		}
	}
	return nil
}

// unblockResolved releases the dependents of a blocker whose transition to a
// resolving status the merge journals, by the peer's rule (unblockTarget).
// It writes the status and journals nothing for the dependent: the peer
// derives it from the blocker's transition. A transition the log cannot carry
// releases nothing, as on a peer.
func (p *mergePlanner) unblockResolved() error {
	for _, id := range p.j.changed {
		after, err := p.storedNode(id)
		if err != nil {
			return err
		}
		if after.Status == p.j.before[id].Status || !isResolvingStatus(model.Status(after.Status)) {
			continue
		}
		if ok, err := p.eligible(id); err != nil || !ok {
			if err != nil {
				return err
			}
			continue
		}
		if err := p.releaseDependents(id); err != nil {
			return err
		}
	}
	return nil
}

func (p *mergePlanner) releaseDependents(blockerID string) error {
	var dependents []string
	// Every task the blocker blocks.
	rows, err := p.tx.QueryContext(p.ctx, `SELECT to_id FROM dependencies WHERE from_id = ? AND dep_type = ?`,
		blockerID, string(model.DepTypeBlocks))
	if err != nil {
		return fmt.Errorf("read tasks blocked by %s: %w", blockerID, err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return fmt.Errorf("scan task blocked by %s: %w", blockerID, err)
		}
		dependents = append(dependents, id)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tasks blocked by %s: %w", blockerID, err)
	}
	for _, id := range dependents {
		restoreTo, ok, err := unblockTarget(p.ctx, p.tx, id)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		// The release a peer derives from the blocker's transition: no event for the dependent.
		if _, err := p.tx.ExecContext(p.ctx, `UPDATE nodes SET status = ?, previous_status = NULL, updated_at = ?
			WHERE id = ? AND deleted_at IS NULL`, string(restoreTo), p.s.clock().UTC().Format(time.RFC3339), id); err != nil {
			return fmt.Errorf("release %s: %w", id, err)
		}
		if p.j.unblocked == nil {
			p.j.unblocked = map[string]bool{}
		}
		p.j.unblocked[id] = true
	}
	return nil
}
