// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"strings"

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
	detail := fmt.Sprintf(
		"%d pulled events quarantined, not applied; retried on every pull, so run 'mtix sync pull' first. If they remain, list them with their reasons: %s",
		n, quarantineInspectCmd)
	held, err := uidHeldReasonQuarantined(ctx, st)
	if err != nil {
		return false, detail + " (" + err.Error() + ")"
	}
	if held {
		detail += heldUIDAction
	}
	return false, detail
}

// heldUIDAction is the doctor's next step for a pulled create quarantined
// with the reason "uid <u> is held by local task <id>" (MTIX-95.31.8).
const heldUIDAction = ". For a reason 'uid <u> is held by local task <id>': run 'mtix show <id>' and compare it with the" +
	" quarantined event. A uid names one task (ADR-003); no mtix command resolves this in this version (MTIX-95.31.8.1), so escalate to the project maintainer" +
	" with the uid, the task id and the event id, and the local task is never deleted; see the admin skill," +
	" 'Quarantined Pulled Events: uid held by a local task'"

// uidHeldReasonQuarantined reports whether any quarantined event carries the
// uid-held reason.
func uidHeldReasonQuarantined(ctx context.Context, st *sqlite.Store) (bool, error) {
	var after *sqlite.QuarantineKey
	for {
		page, err := st.QuarantinePage(ctx, after, 500)
		if err != nil {
			return false, fmt.Errorf("read quarantine reasons: %w", err)
		}
		if len(page) == 0 {
			return false, nil
		}
		for _, q := range page {
			if strings.Contains(q.Reason, "is held by local task") {
				return true, nil
			}
		}
		last := page[len(page)-1]
		after = &sqlite.QuarantineKey{Lamport: last.Lamport, EventID: last.EventID}
	}
}
