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
		if stats.Reclaimed > 0 || stats.Capped > 0 {
			return wakeReclaimResult(stats, detail)
		}
		return DoctorResult{Status: DoctorInfo, Detail: detail}
	}}
}

// wakeReclaimResult reports events a crashed claimant left unfinished (MTIX-112.40.13). The
// first redelivery of such an event is the one permitted duplicate; a capped event was not
// retried again and its message is still in the inbox.
func wakeReclaimResult(stats model.WakeLedgerStats, detail string) DoctorResult {
	return DoctorResult{Status: DoctorWarn,
		Detail: fmt.Sprintf("%d wake(s) were reclaimed-after-crash (a crash between push and record can wake a session twice) and %d closed at reclaim-cap. %s", stats.Reclaimed, stats.Capped, detail),
		Data:   map[string]any{"reclaimed": stats.Reclaimed, "capped": stats.Capped},
		Fix:    "Read the inbox with mtix inbox: a reclaim-cap message was not woken again but is still unread. If reclaims keep appearing, stop the listener or dispatcher that keeps dying, then run mtix doctor. This warning counts retained wake rows and this build has no command that prunes them, so it does not clear by itself: it is healthy when the counts stop growing between doctor runs."}
}
