// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// WakeBudgetReader supplies noncreating local accounting diagnostics.
type WakeBudgetReader interface {
	WakeBudgetSource(context.Context) (string, error)
	WakeBudgetCounters(context.Context) ([]model.WakeBudgetCounter, error)
}

// WakeBudgetDoctorCheck reports counter rewinds without healing or resetting them.
func WakeBudgetDoctorCheck(store WakeBudgetReader, state *oplocal.State) DoctorCheck {
	return DoctorCheck{ID: "budget.rewind", Run: func(ctx context.Context) DoctorResult {
		if store == nil || state == nil {
			return wakeBudgetDoctorFailure(fmt.Errorf("accounting capabilities are required: %w", model.ErrInvalidInput))
		}
		stats, err := inspectWakeBudget(ctx, store, state)
		if err != nil {
			return wakeBudgetDoctorFailure(err)
		}
		row := DoctorResult{Status: DoctorInfo, Detail: "No protected recipient accounting exists."}
		if stats.recipients == 0 {
			return row
		}
		row.Detail = fmt.Sprintf("Protected recipient accounting: %d recipients; %d local counters below their protected floor; %d recipients retain rewind diagnostics; %d interrupted acceptances remain charged.",
			stats.recipients, stats.lower, stats.recorded, stats.pending)
		row.Data = map[string]any{"recipients": stats.recipients, "lower_store_counters": stats.lower,
			"recorded_rewind_recipients": stats.recorded, "pending_acceptances": stats.pending}
		if stats.lower > 0 || stats.recorded > 0 {
			row.Status = DoctorWarn
			row.Fix = "Preserve operator-local accounting. Inspect the restored board; the dispatcher reconciles counters before the next eligible attempt without returning spent allowance. Run mtix doctor --json again."
		}
		return row
	}}
}

type wakeBudgetDiagnostics struct{ recipients, lower, recorded, pending int }

func inspectWakeBudget(ctx context.Context, store WakeBudgetReader, state *oplocal.State) (wakeBudgetDiagnostics, error) {
	var stats wakeBudgetDiagnostics
	mirrors, err := state.WakeBudgetMirrors(ctx)
	if err != nil || len(mirrors) == 0 {
		return stats, err
	}
	source, err := store.WakeBudgetSource(ctx)
	if err != nil {
		return stats, err
	}
	rows, err := store.WakeBudgetCounters(ctx)
	if err != nil {
		return stats, err
	}
	local := map[model.WakeRecipient]int64{}
	for _, row := range rows {
		if row.Source != source || row.Total < 0 {
			return stats, fmt.Errorf("local wake counter metadata conflicts: %w", model.ErrInvalidInput)
		}
		local[row.Recipient] = row.Total
	}
	stats.recipients = len(mirrors)
	for _, mirror := range mirrors {
		if mirror.Sources[source].Committed > local[mirror.Recipient] {
			stats.lower++
		}
		if mirror.Rewinds > 0 {
			stats.recorded++
		}
		for _, row := range mirror.Sources {
			if row.Pending != "" {
				stats.pending++
			}
		}
	}
	return stats, nil
}

func wakeBudgetDoctorFailure(err error) DoctorResult {
	return DoctorResult{Status: DoctorFail, Detail: "Wake accounting cannot be read: " + err.Error(),
		Fix: "Preserve protected budget records; repair the reported access or data problem, then run mtix doctor --json. Never delete the mirror to return allowance."}
}
