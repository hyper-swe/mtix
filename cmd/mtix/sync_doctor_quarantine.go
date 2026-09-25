// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// quarantineCheckName is the name of the doctor's quarantine check.
const quarantineCheckName = "quarantined events"

// quarantineInspectCmd is the read-only command that lists the
// quarantine; the doctor's detail and the docs print it.
const quarantineInspectCmd = "mtix sync quarantine list"

// appendQuarantineCheck adds the doctor's "quarantined events" check
// (MTIX-95.11). It is local only and fails like the other local checks
// when there is no local store.
func appendQuarantineCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		return appendCheck(r, quarantineCheckName, false, "local store not initialized")
	}
	ok, detail := checkQuarantinedEvents(ctx, st)
	return appendCheck(r, quarantineCheckName, ok, detail)
}

// checkQuarantinedEvents passes when no pulled event is quarantined. It
// fails while any is: those events are not applied, so this replica lacks
// them. The detail gives the count, says every pull retries them, the next
// step (pull, which retries them) and the read-only command that lists
// them with their reasons.
func checkQuarantinedEvents(ctx context.Context, st *sqlite.Store) (bool, string) {
	n, err := st.CountQuarantined(ctx)
	if err != nil {
		return false, err.Error()
	}
	if n == 0 {
		return true, "ok"
	}
	return false, fmt.Sprintf(
		"%d pulled events quarantined, not applied; retried on every pull, so run 'mtix sync pull' first. If they remain, list them with their reasons: %s",
		n, quarantineInspectCmd)
}
