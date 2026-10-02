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
	r = appendQuarantineCheck(ctx, r, st)
	return appendUniqueUIDCheck(ctx, r, st)
}

// uniqueUIDCheckName is the name of the doctor's unique-uid check.
const uniqueUIDCheckName = "unique node uids"

// appendUniqueUIDCheck adds the doctor's "unique node uids" check
// (MTIX-95.31.8): it fails while two nodes share a uid, naming each uid, its
// nodes and the exact recovery, and passes otherwise. Local only.
func appendUniqueUIDCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		return appendCheck(r, uniqueUIDCheckName, false, "local store not initialized")
	}
	report, err := st.DuplicateNodeUIDsReport(ctx)
	if err != nil {
		return appendCheck(r, uniqueUIDCheckName, false, err.Error())
	}
	if report != "" {
		return appendCheck(r, uniqueUIDCheckName, false, report)
	}
	return appendCheck(r, uniqueUIDCheckName, true, "ok")
}
