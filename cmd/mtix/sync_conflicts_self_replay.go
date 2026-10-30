// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// selfReplayCheckName is the name of the doctor's self-replay conflict
// rows check.
const selfReplayCheckName = "self-replay conflict rows"

// selfReplayFix is the check's fix: nothing to run. The rows are excluded
// from the unresolved count at read time (readConflicts, MTIX-216).
const selfReplayFix = "none required; inspect with mtix sync conflicts list --all"

// appendSelfReplayConflictCheck adds the doctor's informational
// "self-replay conflict rows" check (MTIX-216). It is local only, always
// passes when the store can be read, and fails like the other local checks
// when there is no local store.
func appendSelfReplayConflictCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	if st == nil {
		return appendCheck(r, selfReplayCheckName, false, "local store not initialized")
	}
	rows, err := readConflicts(ctx, st, "", true)
	if err != nil {
		return appendCheck(r, selfReplayCheckName, false, err.Error())
	}
	n := 0
	for _, row := range rows {
		if row.SelfReplay {
			n++
		}
	}
	if n == 0 {
		return appendCheck(r, selfReplayCheckName, true, "ok: no self-replay conflict rows")
	}
	return appendDoctorCheck(r, DoctorCheck{
		Name: selfReplayCheckName,
		Pass: true,
		Detail: fmt.Sprintf("%d conflict rows were written before 0.5.4, when a pull replayed this store's own "+
			"pushed events; both events were emitted here, so they are not conflicts and are not counted as "+
			"unresolved", n),
		Fix: selfReplayFix,
	})
}
