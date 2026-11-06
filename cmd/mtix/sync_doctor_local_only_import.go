// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// localOnlyImportCheckName is the doctor check for imported changes that
// stay local (MTIX-112.17.16.16).
const localOnlyImportCheckName = "local-only imported changes"

// localOnlyImportFix is what the check prints: the exact commands that make
// the check pass. The imported values were never sent to the hub, so a pull
// may replace them; the commands name the two ways out.
const localOnlyImportFix = "to keep a value: edit the task again with mtix update <id> so the change reaches the hub " +
	"(a task with a later change drops off this list); to take the hub's value: run mtix sync pull"

// appendLocalOnlyImportCheck adds the "local-only imported changes" check: a
// WARN while an automatic import has changed a task the hub already knows and
// nothing since has sent or replaced the change. It reads the local store
// only and contacts no hub. With --json the check carries the bounded id list
// in local_only and the full count in its detail.
func appendLocalOnlyImportCheck(ctx context.Context, r DoctorReport, st *sqlite.Store) DoctorReport {
	live, overflow, err := st.LocalOnlyImports(ctx)
	if err != nil {
		return appendCheck(r, localOnlyImportCheckName, false, "could not read the local-only imports: "+err.Error())
	}
	total := len(live) + overflow
	if total == 0 {
		return appendCheck(r, localOnlyImportCheckName, true, "no imported change is waiting local only")
	}
	ids := make([]string, 0, sqlite.MaxNamedNodes)
	for i := 0; i < len(live) && i < sqlite.MaxNamedNodes; i++ {
		ids = append(ids, live[i].ID)
	}
	detail := fmt.Sprintf("%d task(s) the hub knows were changed by an automatic import and the change was not sent to the hub; "+
		"a pull of a hub edit may replace these values. First %d: %v", total, len(ids), ids)
	return appendDoctorCheck(r, DoctorCheck{Name: localOnlyImportCheckName, Pass: true, Warn: true,
		Detail: detail, Fix: localOnlyImportFix, LocalOnly: ids})
}
