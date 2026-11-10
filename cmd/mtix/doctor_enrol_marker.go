// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/service"
)

// enrolFixTimeBound tells the operator the printed recovery was chosen for the
// data directory as doctor saw it: a command picked for an absent data
// directory is wrong once one exists, and the reverse (wi-28).
const enrolFixTimeBound = "the printed command was chosen for the data directory as it is now and is valid only until that directory changes; run mtix doctor again after any change to it"

// registerEnrolInterruptedDoctor adds board.enrol-interrupted when a worktree
// enrolment left its durable marker behind (the process died between the move of
// the store and the end of the enrolment). It prints the exact recovery
// (MTIX-112.17.16, ADR-007 section 4.2).
func registerEnrolInterruptedDoctor(svc *service.DoctorService, mtixDir string) error {
	marker, err := service.ReadEnrolMarker(mtixDir)
	if errors.Is(err, service.ErrEnrolMarkerUnreadable) {
		return registerEnrolTornMarkerDoctor(svc, mtixDir, err)
	}
	if err != nil {
		return fmt.Errorf("read enrolment marker: %w", err)
	}
	if marker == nil {
		return nil
	}
	return svc.Register(service.DoctorCheck{ID: "board.enrol-interrupted", Run: func(context.Context) service.DoctorResult {
		fix := enrolInterruptedFix(mtixDir, marker)
		detail := "a worktree enrolment was interrupted; " + marker.RecoveryHint()
		if !strings.HasPrefix(fix, "STOP:") { // STOP prose prints no command
			detail += "; " + enrolFixTimeBound
		}
		return service.DoctorResult{
			Status: service.DoctorFail,
			Detail: detail,
			Fix:    fix,
			Data:   map[string]any{"aside_dir": marker.Aside, "backup_path": marker.Backup, "started_at": marker.Started, "pid": marker.PID},
		}
	}})
}

// enrolInterruptedFix is the exact recovery for the marker. When the store
// never moved (a crash between the marker and the move) the live store is
// already in place and only the marker goes; moving it aside would strand it.
// When it moved and no data directory exists (a crash right after the move)
// there is no partial store to keep, and moving a missing directory would stop
// the recovery; otherwise the partial store is kept aside, never deleted. When
// the enrolment lock is unprobeable or held, an enrolment may be live and the
// fix is STOP prose without mv or rm (MTIX-112.17.16.21.2.4).
func enrolInterruptedFix(mtixDir string, marker *service.EnrolMarker) string {
	if fix := liveEnrolmentFix(mtixDir); fix != "" {
		return fix // a possibly-live enrolment never gets a store-moving command
	}
	markerPath := shellQuote(filepath.Join(mtixDir, "enroll.d", "in-progress.json"))
	if !marker.Moved() {
		return "rm " + markerPath
	}
	data := filepath.Join(mtixDir, "data")
	aside := shellQuote(marker.Aside)
	// The original store goes back, so an accept-hub record that names it is
	// stale: it is removed with the marker (rm -f: there may be none).
	accepted := shellQuote(service.HubAcceptedPath(mtixDir))
	if _, err := os.Lstat(data); err != nil { //nolint:gosec // G703: data is the worktree's own .mtix/data, joined from the discovered .mtix directory
		return fmt.Sprintf("mv %s %s && rm %s && rm -f %s", aside, shellQuote(data), markerPath, accepted)
	}
	return fmt.Sprintf("mv %s %s && mv %s %s && rm %s && rm -f %s", shellQuote(data), shellQuote(data+".kept-aside"), aside, shellQuote(data), markerPath, accepted)
}

// registerEnrolHubAcceptedDoctor adds board.enrol-hub-accepted while an
// enrolment that took the hub's version of a differing board is not cleared:
// the old store, its backup and the diff report are the only copy of what the
// hub did not hold (MTIX-112.17.16.6). The fix removes the record once the
// operator has read the report and kept what they need.
func registerEnrolHubAcceptedDoctor(svc *service.DoctorService, mtixDir string) error {
	recPath := shellQuote(service.HubAcceptedPath(mtixDir))
	rec, err := service.ReadHubAccepted(mtixDir)
	if err != nil {
		// A torn record must not take the whole doctor down: it is a row that
		// fails with the exact fix.
		return svc.Register(service.DoctorCheck{ID: "board.enrol-hub-accepted", Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorFail,
				Detail: "the accept-hub record cannot be read (" + format.SafeLine(err.Error()) + "); it is a leftover, and the diff report and the old store it named are kept",
				Fix:    "rm " + recPath}
		}})
	}
	if rec == nil {
		return nil
	}
	return svc.Register(service.DoctorCheck{ID: "board.enrol-hub-accepted", Run: func(context.Context) service.DoctorResult {
		data := map[string]any{"report": rec.Report, "aside_dir": rec.AsideDir, "backup_path": rec.BackupPath, "accepted_at": rec.AcceptedAt}
		if _, statErr := os.Lstat(rec.AsideDir); statErr != nil {
			return service.DoctorResult{Status: service.DoctorWarn, Data: data, Fix: "rm " + recPath,
				Detail: "an accept-hub record names an old store (" + rec.AsideDir + ") that no longer exists, so the enrolment it describes did not complete or the store was cleared; the record is stale"}
		}
		if rec.LostLocalReport != "" {
			data["lost_local_report"] = rec.LostLocalReport
		}
		return service.DoctorResult{
			Status: service.DoctorWarn,
			Detail: enrolAcceptedDetail(rec) + "; keep what you need from them, then clear this warning",
			Fix:    "rm " + recPath,
			Data:   data,
		}
	}})
}

// enrolAcceptedDetail words the warning of an accept-hub record, a lost-value
// record, or both.
func enrolAcceptedDetail(rec *service.HubAccepted) string {
	var parts []string
	if rec.Report != "" && rec.Report != rec.LostLocalReport {
		parts = append(parts, "this worktree was enrolled on the hub's version of a board that differed from it; the node ids and field names that differed are in "+rec.Report)
	}
	if rec.LostLocalReport != "" {
		parts = append(parts, "this worktree was enrolled with the hub's later value where the value written here lost; the node ids and field names are in "+rec.LostLocalReport)
	}
	return strings.Join(parts, "; and ") + "; the old store is in " + rec.AsideDir + " (backup " + rec.BackupPath + ")"
}

// registerEnrolDoctorChecks registers both worktree enrolment rows.
func registerEnrolDoctorChecks(svc *service.DoctorService, mtixDir string) error {
	if err := registerEnrolInterruptedDoctor(svc, mtixDir); err != nil {
		return fmt.Errorf("register enrolment marker check: %w", err)
	}
	if err := registerEnrolHubAcceptedDoctor(svc, mtixDir); err != nil {
		return fmt.Errorf("register accept-hub check: %w", err)
	}
	if err := registerEnrolLockDoctor(svc, mtixDir); err != nil {
		return fmt.Errorf("register enrolment lock check: %w", err)
	}
	return nil
}
