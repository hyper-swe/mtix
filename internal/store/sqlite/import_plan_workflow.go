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

// planWorkflow finds every task whose workflow values differ between the
// store, as the merge leaves it, and the file, and settles each with the
// caller's resolution, as planWorkflow does inside the import. A merge only. It
// returns a *WorkflowConflictError (counts only; the conflicts are in the plan)
// when one is left unsettled, and ErrInvalidInput when the resolution names a
// task that does not conflict (MTIX-95.31.13). Reads only.
func (p *ImportPlan) planWorkflow(ctx context.Context) error {
	if p.opts.Mode != ImportModeMerge {
		return nil
	}
	if err := validateResolution(p.opts.Workflow); err != nil {
		return err
	}
	carries := carriesNodeColumns(p.st.Header.SchemaVersion)
	for ord := 0; ord < p.st.NodeCount(); ord++ {
		if err := p.workflowConflictAt(ctx, ord, carries); err != nil {
			return err
		}
	}
	return p.settleWorkflow(ctx)
}

// workflowConflictAt records the conflict of the staged node at ord against the
// local node that ends at its id, if there is one (workflowConflictOf).
func (p *ImportPlan) workflowConflictAt(ctx context.Context, ord int, carries bool) error {
	f, err := p.incomingAt(ctx, ord)
	if err != nil {
		return err
	}
	l, held, err := p.localBy(ctx, localByFinal, f.fID)
	if err != nil || !held {
		return err
	}
	local, err := scanExportNode(p.s.readDB.QueryRowContext(ctx, planLocalNodeSQL, l.id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read local node %s: %w", l.id, err)
	}
	local.ID, local.UID = f.fID, l.uid // as the merge leaves it: moved, and given its minted uid
	n, err := p.st.stagedNodeAt(ctx, p.q, ord)
	if err != nil {
		return err
	}
	n.ID, n.UID, n.ParentID, n.Seq = f.fID, f.fUID, f.fParent, f.fSeq // as the reconcile rewrote it
	return p.recordWorkflow(ctx, &local, &n, carries)
}

// recordWorkflow stores the conflict of file node n against local, if any.
func (p *ImportPlan) recordWorkflow(ctx context.Context, local, n *exportNode, carries bool) error {
	if refuseDifferentTask(local, n) != nil {
		return nil
	}
	diffs := workflowDiffs(local, n)
	if len(diffs) == 0 {
		return nil
	}
	if err := addWorkflowHistory(ctx, p.s.readDB, local, n, diffs); err != nil {
		return err
	}
	raw, err := encodeWorkflowConflict(WorkflowConflict{ID: n.ID, Title: local.Title, Fields: diffs, Hint: activityHint(local, n, carries)})
	if err != nil {
		return fmt.Errorf("encode workflow conflict %s: %w", n.ID, err)
	}
	return p.add(ctx, secWorkflow, reportRow{c: [5]string{raw, "", n.ID}, n: boolInt(n.UID != "" && n.UID == local.UID)})
}

// boolInt is 1 for true and 0 for false.
func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// settleWorkflow applies the resolution to the recorded conflicts: a listed
// task takes its side, the rest take Prefer. A listed task that does not
// conflict, or a conflict left without a choice, refuses the merge
// (settleWorkflow). Refused or not, the choices are on the rows.
func (p *ImportPlan) settleWorkflow(ctx context.Context) error {
	r := p.opts.Workflow
	if p.counts[secWorkflow] == 0 && len(r.Theirs)+len(r.Ours) == 0 {
		return nil
	}
	if err := p.setChoice(ctx, "", r.Prefer); err != nil {
		return err
	}
	for _, side := range []struct {
		ids    []string
		choice WorkflowChoice
	}{{r.Theirs, WorkflowTheirs}, {r.Ours, WorkflowOurs}} {
		for _, id := range side.ids {
			if err := p.setChoice(ctx, id, side.choice); err != nil {
				return err
			}
		}
	}
	return p.refuseUnsettled(ctx)
}

// setChoice gives the conflicts of task id (all of them when id is empty) the
// choice; a named task with no conflict is ErrInvalidInput.
func (p *ImportPlan) setChoice(ctx context.Context, id string, choice WorkflowChoice) error {
	if choice == "" {
		return nil
	}
	var res sql.Result
	var err error
	if id == "" {
		res, err = p.q.ExecContext(ctx, planChoiceAllSQL, string(choice), int(secWorkflow))
	} else { // the last conflict of the task: the in-memory plan keys them by id
		res, err = p.q.ExecContext(ctx, planChoiceTaskSQL, string(choice), int(secWorkflow), id)
	}
	if err != nil {
		return fmt.Errorf("settle the workflow conflicts: %w", err)
	}
	if n, err := res.RowsAffected(); id != "" && (err != nil || n == 0) {
		return errors.Join(err, fmt.Errorf("task %s has no workflow conflict to resolve (%s): %w", id, choice, model.ErrInvalidInput))
	}
	return nil
}

// refuseUnsettled returns the refusal when a conflict has no choice, and shows
// the conflicts in the report; otherwise it counts the no-ops they replace.
func (p *ImportPlan) refuseUnsettled(ctx context.Context) error {
	var open int
	if err := p.q.QueryRowContext(ctx, planCountOpenSQL, int(secWorkflow)).Scan(&open, &p.wfIdempotent); err != nil {
		return fmt.Errorf("count the workflow conflicts: %w", err)
	}
	if open == 0 {
		return nil
	}
	p.wfShown = true
	return &WorkflowConflictError{total: p.counts[secWorkflow], open: open}
}
