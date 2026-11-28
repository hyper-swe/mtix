// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"
)

// planUIDAdoptions lists every local task a merge gives the file's uid: a file
// node that carries a uid, at an id where a local node holding another uid, or
// none, ends once the planned moves are applied (planUIDAdoptions,
// MTIX-95.31.6). Run after every rewrite of the file.
func (p *ImportPlan) planUIDAdoptions(ctx context.Context) error {
	if p.opts.Mode != ImportModeMerge {
		return nil
	}
	for ord := 0; ord < p.st.NodeCount(); ord++ {
		f, err := p.incomingAt(ctx, ord)
		if err != nil {
			return err
		}
		if f.fUID == "" {
			continue
		}
		l, held, err := p.localWhere(ctx, "final = ?", f.fID)
		if err != nil {
			return err
		}
		if !held || l.uid0 == f.fUID {
			continue
		}
		if err := p.add(ctx, secAdoption, reportRow{c: [5]string{f.fID, l.uid0, f.fUID, l.title, f.title}}); err != nil {
			return err
		}
	}
	return nil
}

// requireConfirmation returns ErrImportConfirmationRequired, before anything
// is written, when the plan renumbers without confirm: a local task
// (MTIX-95.31.4), or an incoming provisional node while the store holds nodes
// (ADR-003 §6).
func (p *ImportPlan) requireConfirmation(ctx context.Context) error {
	if p.opts.Confirm {
		return nil
	}
	if n := p.counts[secLocalRenumber]; n > 0 {
		return fmt.Errorf("%d local node(s) would be renumbered because the file holds a different task under "+
			"their id; review the report and re-run with confirmation: %w", n, ErrImportConfirmationRequired)
	}
	if p.counts[secRemap] == 0 {
		return nil
	}
	empty, err := p.s.storeIsEmpty(ctx)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf("%d provisional node(s) would be renumbered in a live store; "+
			"re-run with confirmation: %w", p.counts[secRemap], ErrImportConfirmationRequired)
	}
	return nil
}
