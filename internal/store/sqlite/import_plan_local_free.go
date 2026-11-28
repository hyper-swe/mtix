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

// nextSeqFreeInBoth returns the number a renumbered local task takes under
// parent: the first number after the highest one held there by a local node
// (before or after the planned moves) or a file node, whose namespace no local
// node and no file node holds (nextSeqFreeInBoth), and takes it.
func (p *ImportPlan) nextSeqFreeInBoth(ctx context.Context, project, parent string) (int, error) {
	var local, file int
	// The highest number a local node holds, now or after its move, under the
	// parent: the nodes whose parent ends at parent, or is parent and not local.
	if err := p.q.QueryRowContext(ctx, `SELECT COALESCE(MAX(MAX(seq, final_seq)), 0) FROM plan_local
		WHERE project = ?1 AND (parent_id IN (SELECT id FROM plan_local WHERE final = ?2)
		  OR (parent_id = ?2 AND NOT EXISTS (SELECT 1 FROM plan_local WHERE id = ?2)))`,
		project, parent).Scan(&local); err != nil {
		return 0, fmt.Errorf("read the highest local number under %q: %w", parent, err)
	}
	// The highest number a file node holds under the parent.
	if err := p.q.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM plan_in WHERE project = ? AND parent_id = ?`,
		project, parent).Scan(&file); err != nil {
		return 0, fmt.Errorf("read the highest file number under %q: %w", parent, err)
	}
	for seq := max(local, file) + 1; ; seq++ {
		held, err := p.namespaceHeld(ctx, model.BuildID(project, parent, seq))
		if err != nil {
			return 0, err
		}
		if !held {
			return seq, p.take(ctx, takenKey(project, parent), seq)
		}
	}
}

// namespaceHeld reports whether a local node or a file node holds id or an id
// under it (a node whose parent is gone keeps its id).
func (p *ImportPlan) namespaceHeld(ctx context.Context, id string) (bool, error) {
	for _, table := range []string{"plan_local", "plan_in"} {
		var one int
		// Whether a node of the table is id or under it (ids from id. up to id/).
		err := p.q.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE id = ?1 OR (id >= ?1 || '.' AND id < ?1 || '/') LIMIT 1`,
			id).Scan(&one)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, fmt.Errorf("check the namespace of %s: %w", id, err)
		}
	}
	return false, nil
}

// checkFinals returns ErrConflict when two local nodes would end at one id
// (checkFinals).
func (p *ImportPlan) checkFinals(ctx context.Context) error {
	var first, second, final string
	// The first local node, in plan order, that ends where an earlier one does.
	err := p.q.QueryRowContext(ctx, `SELECT a.id, b.id, b.final FROM plan_local b
		JOIN plan_local a ON a.final = b.final AND a.pos < b.pos ORDER BY b.pos, a.pos LIMIT 1`).Scan(&first, &second, &final)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check where the local nodes end: %w", err)
	}
	return fmt.Errorf("merge: local nodes %s and %s would both end at %s: %w", first, second, final, model.ErrConflict)
}

// finish lists every local node whose id changes, shallowest first, as moved
// or renumbered (finish). A node under a renumbered one is renumbered.
func (p *ImportPlan) finish(ctx context.Context) error {
	return p.eachLocal(ctx, func(ctx context.Context, l *planLocal) error {
		if l.final == l.id {
			return nil
		}
		sec := secMoved
		if l.renumbered {
			sec = secLocalRenumber
		}
		return p.addRemap(ctx, sec, l.uid, l.id, l.final, l.stamp)
	})
}
