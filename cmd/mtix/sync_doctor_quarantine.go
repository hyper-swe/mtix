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
	check := DoctorCheck{Name: quarantineCheckName, Pass: ok, Detail: detail}
	if !ok && strings.Contains(detail, quarantineInspectCmd) {
		fix, err := quarantineAcknowledgementFix(ctx, st, app.jsonOutput)
		if err != nil {
			check.Detail += "; " + err.Error()
		} else {
			check.Fix = fix
		}
	}
	return appendDoctorCheck(r, check)
}

// checkQuarantinedEvents passes when no unacknowledged pulled event remains.
// Acknowledged events are still retained and unapplied, and their count is shown.
// Read errors and every unresolved quarantine remain failures.
func checkQuarantinedEvents(ctx context.Context, st *sqlite.Store) (bool, string) {
	counts, err := st.CountQuarantineAcknowledgements(ctx)
	if err != nil {
		return false, err.Error()
	}
	if counts.Total == 0 {
		return true, "ok"
	}
	if counts.Unacknowledged == 0 {
		return true, fmt.Sprintf("%d acknowledged pulled events retained, not applied; 0 unacknowledged", counts.Acknowledged)
	}
	return quarantineFailureDetail(ctx, st, counts)
}

func quarantineFailureDetail(ctx context.Context, st *sqlite.Store, counts sqlite.QuarantineCounts) (bool, string) {
	detail := fmt.Sprintf(
		"%d pulled events quarantined, not applied; retried on every pull, so run 'mtix sync pull' first. If they remain, list them with their reasons: %s",
		counts.Total, quarantineInspectCmd)
	if counts.Acknowledged > 0 {
		detail += fmt.Sprintf("; %d acknowledged, %d unacknowledged", counts.Acknowledged, counts.Unacknowledged)
	}
	held, err := uidHeldReasonQuarantined(ctx, st)
	if err != nil {
		return false, detail + " (" + err.Error() + ")"
	}
	if held {
		detail += heldUIDAction
	}
	invalid, err := st.CountQuarantinedInvalidIDs(ctx)
	if err != nil {
		return false, detail + " (" + err.Error() + ")"
	}
	if invalid > 0 {
		detail += fmt.Sprintf(" (%d of them for an invalid node id)", invalid) + invalidIDQuarantineAction
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
