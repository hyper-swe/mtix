// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// appendLocalStoreChecks adds the doctor's checks that read only the local
// store and follow the queue check: "no orphan applied" (check 4) and
// "quarantined events" (check 5, MTIX-95.11). Each fails with "local store
// not initialized" when there is no store. Kept out of runSyncDoctor so it
// stays within the function length limit.
func appendLocalStoreChecks(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		r = appendCheck(r, "no orphan applied", false, "local store not initialized")
	} else {
		orphanOK, detail := checkNoOrphanApplied(ctx, st)
		r = appendCheck(r, "no orphan applied", orphanOK, detail)
	}
	return appendQuarantineCheck(ctx, r, st)
}
