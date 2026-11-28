// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
)

// planLocal is one row of plan_local: a local node, live or soft-deleted, with
// the id and number the merge leaves it at (localNode, on disk).
type planLocal struct {
	pos                                       int
	id, uid0, uid, project, parent, createdAt string
	title, final                              string
	seq, finalSeq                             int
	stamp, renumbered                         bool
}

func scanPlanLocal(row rowScanner) (planLocal, error) {
	var l planLocal
	err := row.Scan(&l.pos, &l.id, &l.uid0, &l.uid, &l.project, &l.parent, &l.seq, &l.createdAt, &l.title,
		&l.stamp, &l.final, &l.finalSeq, &l.renumbered)
	return l, err
}

// localKey names the column a plan_local row is looked up by.
type localKey int

const (
	localByPos   localKey = iota // the position in plan order
	localByID                    // the id the node has now
	localByFinal                 // the id the plan leaves it at
)

// localQuery returns the constant statement that looks local rows up by key.
func localQuery(key localKey) string {
	switch key {
	case localByID:
		return planLocalByIDSQL
	case localByFinal:
		return planLocalByFinalSQL
	default:
		return planLocalByPosSQL
	}
}

// localBy reads the plan_local row the key selects, if any.
func (p *ImportPlan) localBy(ctx context.Context, key localKey, arg any) (planLocal, bool, error) {
	l, err := scanPlanLocal(p.q.QueryRowContext(ctx, localQuery(key), arg))
	if errors.Is(err, sql.ErrNoRows) {
		return l, false, nil
	}
	if err != nil {
		return l, false, fmt.Errorf("read local plan row: %w", err)
	}
	return l, true, nil
}

// loadLocal copies every local node, soft-deleted ones included, shallowest
// first and then by id, into plan_local (loadLocalNodes, on disk).
func (p *ImportPlan) loadLocal(ctx context.Context) error {
	rows, err := p.s.readDB.QueryContext(ctx, planLoadLocalSQL)
	if err != nil {
		return fmt.Errorf("read the local nodes for the merge plan: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			p.s.logger.Error("failed to close merge plan rows", "error", closeErr)
		}
	}()
	for rows.Next() {
		var l planLocal
		if err := rows.Scan(&l.id, &l.uid0, &l.project, &l.parent, &l.seq, &l.createdAt, &l.title); err != nil {
			return fmt.Errorf("read the local nodes for the merge plan: %w", err)
		}
		if _, err := p.q.ExecContext(ctx, planInsertLocalSQL,
			p.localCount, l.id, l.uid0, l.uid0, l.project, l.parent, l.seq, l.createdAt, l.title, l.id, l.seq); err != nil {
			return fmt.Errorf("copy a local node to the merge plan: %w", err)
		}
		p.localCount++
	}
	return rows.Err()
}

// stampMissingUIDs gives every local node without a uid a backfill uid before
// any identity is decided (stampMissingUIDs, MTIX-95.31.9).
func (p *ImportPlan) stampMissingUIDs(ctx context.Context) error {
	for last := -1; ; {
		var pos int
		var id string
		err := p.q.QueryRowContext(ctx, planStampNextSQL, last).Scan(&pos, &id)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("find a local task without a uid: %w", err)
		}
		uid, err := model.NewBackfillUID()
		if err != nil {
			return fmt.Errorf("mint a uid for local task %s before it is renumbered: %w", id, err)
		}
		if _, err := p.q.ExecContext(ctx, planStampSetSQL, uid, pos); err != nil {
			return fmt.Errorf("record the uid minted for %s: %w", id, err)
		}
		last = pos
	}
}

// saveFinal stores where the merge leaves local node l.
func (p *ImportPlan) saveFinal(ctx context.Context, l *planLocal) error {
	if _, err := p.q.ExecContext(ctx, planSaveFinalSQL, l.final, l.finalSeq, l.renumbered, l.pos); err != nil {
		return fmt.Errorf("record where %s ends: %w", l.id, err)
	}
	return nil
}

// finalOf returns the id local node id ends at, or id when it is not local.
func (p *ImportPlan) finalOf(ctx context.Context, id string) (string, error) {
	up, ok, err := p.localBy(ctx, localByID, id)
	if err != nil || !ok {
		return id, err
	}
	return up.final, nil
}

// place sets l's final id under its parent's final id (place).
func (p *ImportPlan) place(ctx context.Context, l *planLocal) (string, error) {
	parent, err := p.finalOf(ctx, l.parent)
	if err != nil {
		return "", err
	}
	switch {
	case l.finalSeq != l.seq:
		l.final = model.BuildID(l.project, parent, l.finalSeq)
	case l.parent != "" && parent != l.parent && strings.HasPrefix(l.id, l.parent+"."):
		l.final = parent + l.id[len(l.parent):]
	default:
		l.final = l.id
	}
	return parent, nil
}

// planLocalRenumbers plans where a merge puts the local tasks the file numbers
// differently (planLocalRenumbers, MTIX-95.31.4): moves, then renumbers.
func (p *ImportPlan) planLocalRenumbers(ctx context.Context) error {
	if p.opts.Mode != ImportModeMerge {
		return nil
	}
	if err := p.loadLocal(ctx); err != nil {
		return err
	}
	if err := p.stampMissingUIDs(ctx); err != nil { // MTIX-95.31.9: before any identity decision
		return err
	}
	if err := p.eachLocal(ctx, p.follow); err != nil {
		return err
	}
	if err := p.eachLocal(ctx, p.renumber); err != nil {
		return err
	}
	if err := p.checkFinals(ctx); err != nil {
		return err
	}
	return p.finish(ctx)
}

// eachLocal runs fn on every local node in plan order, reading each row when
// its turn comes so earlier rows' updates are seen.
func (p *ImportPlan) eachLocal(ctx context.Context, fn func(context.Context, *planLocal) error) error {
	for pos := 0; pos < p.localCount; pos++ {
		l, ok, err := p.localBy(ctx, localByPos, pos)
		if err != nil || !ok {
			return errors.Join(err, notFoundIf(!ok, "local plan row"))
		}
		if err := fn(ctx, &l); err != nil {
			return err
		}
	}
	return nil
}

// notFoundIf returns an ErrNotFound naming what when missing is true.
func notFoundIf(missing bool, what string) error {
	if missing {
		return fmt.Errorf("%s: %w", what, model.ErrNotFound)
	}
	return nil
}

// follow places the local node under its parent's final id, and moves it when
// the file holds its uid under another id there (follow).
func (p *ImportPlan) follow(ctx context.Context, l *planLocal) error {
	parent, err := p.place(ctx, l)
	if err != nil {
		return err
	}
	f, ok, err := p.incomingBy(ctx, incomingByFUID, l.uid)
	if err != nil {
		return err
	}
	if !ok || f.id == l.final {
		return p.saveFinal(ctx, l)
	}
	if f.parent != parent || model.BuildID(l.project, parent, f.seq) != f.id {
		if err := p.add(ctx, secConflict, reportRow{c: [5]string{l.uid, f.id, l.id}, n: int(ConflictLocalUIDMismatch)}); err != nil {
			return err
		}
		return p.saveFinal(ctx, l)
	}
	l.finalSeq, l.final = f.seq, f.id
	if err := p.take(ctx, takenKey(l.project, parent), f.seq); err != nil {
		return err
	}
	return p.saveFinal(ctx, l)
}

// renumber moves the local node off an id the file gives to a different
// task, to the next number free in both (renumber).
func (p *ImportPlan) renumber(ctx context.Context, l *planLocal) error {
	up, ok, err := p.localBy(ctx, localByID, l.parent)
	if err != nil {
		return err
	}
	if ok && up.renumbered {
		l.renumbered = true
	}
	parent, err := p.place(ctx, l)
	if err != nil {
		return err
	}
	f, held, err := p.incomingBy(ctx, incomingByID, l.final)
	if err != nil {
		return err
	}
	if !held || !differentTaskAt(l, &f) {
		return p.saveFinal(ctx, l)
	}
	if noteErr := p.noteTitleMismatch(ctx, l, &f); noteErr != nil {
		return noteErr
	}
	if l.finalSeq, err = p.nextSeqFreeInBoth(ctx, l.project, parent); err != nil {
		return err
	}
	l.final, l.renumbered = model.BuildID(l.project, parent, l.finalSeq), true
	return p.saveFinal(ctx, l)
}

// differentTaskAt reports whether the file's node f, at local node l's final
// id, is a different task: by the titles when either has no uid, and by
// differentIdentity otherwise (differentTaskAt, MTIX-95.31.9).
func differentTaskAt(l *planLocal, f *inNode) bool {
	if l.uid == "" || f.fUID == "" {
		return l.title != f.title
	}
	return differentIdentity(taskIdentity{l.uid, l.createdAt}, taskIdentity{f.fUID, f.createdAt})
}

// noteTitleMismatch records a local task renumbered off the id of file node f
// because they have different titles and no uid to compare (MTIX-95.31.9).
func (p *ImportPlan) noteTitleMismatch(ctx context.Context, l *planLocal, f *inNode) error {
	if l.uid != "" && f.fUID != "" {
		return nil
	}
	return p.add(ctx, secTitle, reportRow{c: [5]string{l.final, l.title, f.title}})
}
