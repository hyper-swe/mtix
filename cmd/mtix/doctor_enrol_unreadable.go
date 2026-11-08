// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/service"
)

// registerEnrolTornMarkerDoctor adds board.enrol-interrupted for a marker that
// exists but cannot be decoded. Top-level doctor proceeds past a torn marker
// (CheckEnrolGuard), so the row prints a fix that works when run instead of the
// command exiting with the decode error (MTIX-112.17.16.21.2).
func registerEnrolTornMarkerDoctor(svc *service.DoctorService, mtixDir string, readErr error) error {
	detail := "the enrolment marker cannot be read (" + format.SafeLine(readErr.Error()) + ")"
	fix := tornMarkerFix(mtixDir)
	return svc.Register(service.DoctorCheck{ID: "board.enrol-interrupted", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorFail, Detail: detail, Fix: fix,
			Data: map[string]any{"marker_path": service.EnrolMarkerPath(mtixDir)}}
	}})
}

// tornMarkerFix is the recovery for an unreadable marker. The marker no longer
// names the original store, so the aside directories are searched: none means
// the store never moved and only the marker goes; exactly one is the original
// store and goes back as for a readable marker. With several, the project maintainer must
// choose: the fix is words, never a command, and nothing is picked or moved.
func tornMarkerFix(mtixDir string) string {
	marker := service.EnrolMarkerPath(mtixDir)
	asides, err := service.EnrolAsideDirs(mtixDir)
	switch {
	case err != nil:
		return "the project maintainer must inspect " + mtixDir + " (" + format.SafeLine(err.Error()) + "); nothing was changed"
	case len(asides) == 0:
		return "rm " + shellQuote(marker)
	case len(asides) == 1:
		return enrolInterruptedFix(mtixDir, &service.EnrolMarker{Aside: asides[0]})
	default:
		return "STOP: the project maintainer must choose which of these directories holds the original store: " + strings.Join(asides, ", ") +
			"; no command is printed because choosing wrongly loses data. After choosing, move it to " + mtixDir + "/data and remove " + marker
	}
}

// registerEnrolLockDoctor adds board.enrol-lock when the enroll lock file exists
// but cannot be probed (neither held nor free). It is registered only for that
// state, so a healthy worktree gains no row (MTIX-112.17.16.21.2). The repair is
// the typed doctor --fix: it uses the store's own lock primitive to refuse a
// held lock, so the printed command cannot move a lock a live process holds.
func registerEnrolLockDoctor(svc *service.DoctorService, mtixDir string) error {
	err := service.ProbeEnrolLock(mtixDir)
	if err == nil || !errors.Is(err, service.ErrEnrolLockUnprobeable) {
		return nil
	}
	lock := service.EnrolLockPath(mtixDir)
	detail := "the enrolment lock cannot be probed (" + format.SafeLine(err.Error()) + ")"
	fix := "mtix doctor --fix"
	if info, statErr := os.Lstat(lock); statErr == nil && info.Mode().IsRegular() {
		// A regular file that cannot be probed cannot be proven unheld: no command
		// is safe, so the row asks the project maintainer and the repair would refuse anyway.
		fix = "the project maintainer must make " + lock + " readable and writable by the current user, or stop the process that holds it; then run mtix doctor"
	}
	return svc.Register(service.DoctorCheck{ID: "board.enrol-lock",
		Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorFail, Detail: detail, Fix: fix,
				Data: map[string]any{"lock_path": lock}}
		},
		Fixer: &service.DoctorFixer{Kind: service.DoctorRepairNonGrant, Apply: func(context.Context) error {
			return service.MoveAsideUnprobeableEnrolLock(mtixDir)
		}}})
}
