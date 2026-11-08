// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
	fix := tornMarkerFix(mtixDir, readErr)
	return svc.Register(service.DoctorCheck{ID: "board.enrol-interrupted", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorFail, Detail: detail, Fix: fix,
			Data: map[string]any{"marker_path": service.EnrolMarkerPath(mtixDir)}}
	}})
}

// tornMarkerFix is the recovery for an unreadable marker. The marker no longer
// names the original store, so the aside directories are searched: none means
// the store never moved and only the marker goes. Exactly one aside goes back
// only when the live data directory is missing or empty, which is the evidence
// that it is this enrolment's original: a successful enrolment leaves its aside
// behind, so beside a live store a single aside may be a stale old store. In
// every other case the project maintainer must choose: the fix is words, never a
// command, and nothing is picked or moved (MTIX-112.17.16.21.2).
//
// A marker that could not be read for a reason other than its content (another
// user's 0600 marker, an I/O error), or any lock state other than free, means an
// enrolment may be live: the row prints STOP prose and no command, whatever the
// asides look like (MTIX-112.17.16.21.2.4).
func tornMarkerFix(mtixDir string, readErr error) string {
	marker := service.EnrolMarkerPath(mtixDir)
	if !errors.Is(readErr, service.ErrEnrolMarkerUndecodable) {
		return enrolMayBeLiveFix(mtixDir, "the marker could not be read ("+format.SafeLine(readErr.Error())+"), so it may belong to a live enrolment")
	}
	if fix := liveEnrolmentFix(mtixDir); fix != "" {
		return fix
	}
	asides, err := service.EnrolAsideDirs(mtixDir)
	switch {
	case err != nil:
		return "the project maintainer must inspect " + mtixDir + " (" + format.SafeLine(err.Error()) + "); nothing was changed"
	case len(asides) == 0:
		return "rm " + shellQuote(marker)
	case len(asides) == 1 && liveStoreAbsent(filepath.Join(mtixDir, "data")):
		return enrolInterruptedFix(mtixDir, &service.EnrolMarker{Aside: asides[0]})
	default:
		return "STOP: the project maintainer must choose which of these directories holds the original store: " + strings.Join(asides, ", ") +
			"; no command is printed because choosing wrongly loses data" + staleAsideNote(len(asides)) +
			". After choosing, move it to " + mtixDir + "/data (keeping the current data directory aside, never deleting it) and remove " + marker
	}
}

// liveEnrolmentFix returns STOP prose when the enrolment lock is not provably
// free (unprobeable, or held), and "" when a command is safe to print.
func liveEnrolmentFix(mtixDir string) string {
	if err := service.ProbeEnrolLock(mtixDir); err != nil {
		return enrolMayBeLiveFix(mtixDir, "the enrolment lock is not free ("+format.SafeLine(err.Error())+")")
	}
	return ""
}

// enrolMayBeLiveFix is the STOP prose for a possibly-live enrolment: no mv, no rm.
func enrolMayBeLiveFix(mtixDir, why string) string {
	return "STOP: an enrolment may still be running, so no command is printed: " + why +
		". See the board.enrol-lock row, and ask the owner of the enrolment or the project maintainer before changing anything in " + mtixDir
}

func staleAsideNote(count int) string {
	if count != 1 {
		return ""
	}
	return " (a live store exists, so this directory may be a stale store from an earlier enrolment rather than the original of this one)"
}

// liveStoreAbsent reports whether the data directory is missing or has no
// entries, so nothing live would be displaced by putting an aside store back.
func liveStoreAbsent(data string) bool {
	entries, err := os.ReadDir(data)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	return err == nil && len(entries) == 0
}

// doctorMustNotOpenStore reports whether top-level doctor has to print its rows
// without creating or opening a store: a torn marker or a lock that cannot be
// probed means an enrolment may be live (a lock owned by another user is
// unprobeable, not held), and it may have moved the store away
// (MTIX-112.17.16.21.2).
func doctorMustNotOpenStore(mtixDir string) bool {
	if errors.Is(service.ProbeEnrolLock(mtixDir), service.ErrEnrolLockUnprobeable) {
		return true
	}
	_, err := service.ReadEnrolMarker(mtixDir)
	return errors.Is(err, service.ErrEnrolMarkerUnreadable)
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
	fix := "mtix doctor --fix"
	if info, statErr := os.Lstat(lock); statErr == nil && info.Mode().IsRegular() {
		// A regular file that cannot be probed cannot be proven unheld: no command
		// is safe, so the row asks the project maintainer and the repair would refuse anyway.
		dir := filepath.Dir(lock)
		fix = "the project maintainer must make " + lock + " readable and writable by the current user, and " + dir +
			" a real directory (not a link) owned by the current user, or stop the process that holds the lock; then run mtix doctor"
	}
	return svc.Register(service.DoctorCheck{ID: "board.enrol-lock",
		Run: func(context.Context) service.DoctorResult {
			// Probe again on every run so a row repaired by --fix shows pass in the same run.
			probeErr := service.ProbeEnrolLock(mtixDir)
			if probeErr == nil {
				return service.DoctorResult{Status: service.DoctorPass, Detail: "the enrolment lock can be probed", Data: map[string]any{"lock_path": lock}}
			}
			return service.DoctorResult{Status: service.DoctorFail, Fix: fix, Data: map[string]any{"lock_path": lock},
				Detail: "the enrolment lock cannot be probed (" + format.SafeLine(probeErr.Error()) + ")"}
		},
		Fixer: &service.DoctorFixer{Kind: service.DoctorRepairNonGrant, Apply: func(context.Context) error {
			return service.MoveAsideUnprobeableEnrolLock(mtixDir)
		}}})
}
