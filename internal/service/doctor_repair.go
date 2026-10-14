// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import "context"

// DoctorRepairKind is an explicit repair capability, never inferred from Fix text.
type DoctorRepairKind uint8

// Only DoctorRepairNonGrant permits execution. Zero, grant and unknown kinds refuse it.
const (
	DoctorRepairNone DoctorRepairKind = iota
	DoctorRepairNonGrant
	DoctorRepairGrant
)

// DoctorFixer pairs an explicit capability with a feature-owned repair.
// Grant callbacks are never executed; their command remains in the result's Fix.
type DoctorFixer struct {
	Kind  DoctorRepairKind
	Apply func(context.Context) error
}

func (s *DoctorService) runCheck(ctx context.Context, check DoctorCheck, opts DoctorRunOptions) DoctorResult {
	row := check.Run(ctx)
	row.ID = check.ID
	if !opts.Fix || check.Fixer == nil || (row.Status != DoctorFail && row.Status != DoctorWarn) || ctx.Err() != nil {
		return row
	}
	if check.Fixer.Kind != DoctorRepairNonGrant || check.Fixer.Apply == nil {
		row.Detail += "; automatic repair refused; run the printed command yourself"
		return row
	}
	if err := check.Fixer.Apply(ctx); err != nil {
		row.Status = DoctorFail
		row.Detail += "; repair failed: " + err.Error()
		return row
	}
	if ctx.Err() != nil {
		return row
	}
	row = check.Run(ctx)
	row.ID = check.ID
	return row
}
