// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// WakeLedgerReader deliberately provides no mutation, pruning or grant capability.
type WakeLedgerReader interface {
	MinDispatchProtocol(context.Context) (int, error)
	WakeLedgerStats(context.Context) (model.WakeLedgerStats, error)
}

// WakeLedgerDoctorCheck reports retained deduplication state without offering repairs.
func WakeLedgerDoctorCheck(reader WakeLedgerReader) DoctorCheck {
	return DoctorCheck{ID: "wake.ledger", Run: func(ctx context.Context) DoctorResult {
		if reader == nil {
			return DoctorResult{Status: DoctorFail, Detail: "Wake ledger backend is unavailable.", Fix: "Run mtix doctor from an opened local board."}
		}
		stats, err := reader.WakeLedgerStats(ctx)
		if err != nil {
			return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("Cannot read retained wake rows: %v", err), Fix: "Restore access to the local board, then run mtix doctor."}
		}
		detail := fmt.Sprintf("Retained %d wake rows (%d claimed) and %d rejected completions. No automatic pruning.", stats.Rows, stats.Claimed, stats.Rejected)
		floor, err := reader.MinDispatchProtocol(ctx)
		if err != nil {
			return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("Cannot read dispatch protocol: %v. %s", err, detail), Fix: "Restore valid dispatch protocol metadata, then run mtix doctor."}
		}
		if floor < 0 {
			return DoctorResult{Status: DoctorFail, Detail: "Invalid dispatch protocol metadata. " + detail,
				Fix: "Restore valid dispatch protocol metadata, then run mtix doctor."}
		}
		if floor > model.AttachProtocol {
			return DoctorResult{Status: DoctorFail, Detail: fmt.Sprintf("Board dispatch protocol %d exceeds this binary's supported protocol %d or is invalid. %s", floor, model.AttachProtocol, detail),
				Fix: fmt.Sprintf("Install a binary supporting dispatch protocol %d, restart the dispatcher, then run mtix doctor.", floor)}
		}
		return DoctorResult{Status: DoctorInfo, Detail: detail}
	}}
}
