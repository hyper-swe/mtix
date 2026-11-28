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
	if err := p.q.QueryRowContext(ctx, planLocalMaxSQL, project, parent).Scan(&local); err != nil {
		return 0, fmt.Errorf("read the highest local number under %q: %w", parent, err)
	}
	if err := p.q.QueryRowContext(ctx, planFileMaxSQL, project, parent).Scan(&file); err != nil {
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

// namespaceQuery returns the constant statement that looks for the namespace in
// the file's nodes (inFile) or in the local ones.
func namespaceQuery(inFile bool) string {
	if inFile {
		return planNamespaceInSQL
	}
	return planNamespaceLocalSQL
}

// namespaceHeld reports whether a local node or a file node holds id or an id
// under it (a node whose parent is gone keeps its id).
func (p *ImportPlan) namespaceHeld(ctx context.Context, id string) (bool, error) {
	for _, inFile := range []bool{false, true} {
		var one int
		err := p.q.QueryRowContext(ctx, namespaceQuery(inFile), id).Scan(&one)
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
	err := p.q.QueryRowContext(ctx, planCheckFinalsSQL).Scan(&first, &second, &final)
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
