// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
)

// A merge on a store that syncs changes no dependency on a task the sync log
// holds and sets no status that dependencies decide (principle H, round 6):
// it never blocks a dependent and never keeps one blocked against the file. The
// one status it derives is the release a peer derives from the blocker's
// journaled transition, by the peer's own rule (unblockTarget, unblockDependents).

// unblockResolved releases the dependents of a blocker whose transition to a
// resolving status the merge journals, by the peer's rule (unblockTarget); a
// dependent the file gave another status is no longer blocked, so it stays.
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
