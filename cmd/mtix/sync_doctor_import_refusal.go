// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	"github.com/hyper-swe/mtix/internal/service"
)

// importRefusalCheckName is the name of the doctor's pending import refusal
// check (MTIX-112.17.16.16).
const importRefusalCheckName = "pending import refusal"

// appendImportRefusalCheck adds the doctor's pending import refusal check. It
// passes when no automatic import of .mtix/tasks.json awaits a decision. A
// pending refusal fails it, naming the kind and the file, and the Fix names the
// ways out (the same text mtix sync prints; where it lists several, the user
// chooses). A refusal record or board that cannot be read fails it too. It
// reads the record through the sync service and is local only.
func appendImportRefusalCheck(r DoctorReport, svc *service.SyncService, mtixDir string) DoctorReport {
	if svc == nil || mtixDir == "" {
		return appendDoctorCheck(r, DoctorCheck{
			Name: importRefusalCheckName, Skipped: true, Detail: "no local project to read the refusal record from",
		})
	}
	st, err := svc.PendingImportRefusal(mtixDir)
	if err != nil {
		return appendDoctorCheck(r, DoctorCheck{
			Name:   importRefusalCheckName,
			Detail: "could not read the import refusal state: " + safeText(oneLine(err.Error())),
			Fix:    unreadableRefusalFix(err),
		})
	}
	if !st.Pending {
		return appendCheck(r, importRefusalCheckName, true, "no automatic import of .mtix/tasks.json is refused")
	}
	// The reason can name task ids and titles read from the board file: print
	// it as plain ASCII so it cannot write control characters to a terminal.
	reason := safeText(oneLine(st.Reason))
	return appendDoctorCheck(r, DoctorCheck{
		Name: importRefusalCheckName,
		Detail: "the automatic import of " + st.File + " was refused (kind " + st.Kind + "): " + reason +
			"; nothing was imported and writing commands leave the file as it is",
		Fix: st.Fix,
	})
}

// unreadableRefusalFix words the fix by what could not be read. The refusal
// record may be moved aside, never deleted; the board .mtix/tasks.json is
// never moved or deleted, because it is the team's board.
func unreadableRefusalFix(err error) string {
	if errors.Is(err, service.ErrRefusalRecordUnreadable) {
		return "move the refusal record named above aside (do not delete it), then run mtix sync to see whether " +
			".mtix/tasks.json differs from the local store"
	}
	return "check the permissions and the type of the path named above (.mtix/tasks.json must be a regular file); " +
		"if the file is damaged or missing, restore it from git; do not move or delete it; then run mtix sync"
}
