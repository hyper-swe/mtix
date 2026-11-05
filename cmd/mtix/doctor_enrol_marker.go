// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/service"
)

// registerEnrolInterruptedDoctor adds board.enrol-interrupted when a worktree
// enrolment left its durable marker behind (the process died between the move of
// the store and the end of the enrolment). It prints the exact recovery
// (MTIX-112.17.16, ADR-007 section 4.2).
func registerEnrolInterruptedDoctor(svc *service.DoctorService, mtixDir string) error {
	marker, err := service.ReadEnrolMarker(mtixDir)
	if err != nil {
		return fmt.Errorf("read enrolment marker: %w", err)
	}
	if marker == nil {
		return nil
	}
	return svc.Register(service.DoctorCheck{ID: "board.enrol-interrupted", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{
			Status: service.DoctorFail,
			Detail: "a worktree enrolment was interrupted; " + marker.RecoveryHint(),
			Fix:    enrolInterruptedFix(mtixDir, marker),
			Data:   map[string]any{"aside_dir": marker.Aside, "backup_path": marker.Backup, "started_at": marker.Started, "pid": marker.PID},
		}
	}})
}

// enrolInterruptedFix is the exact recovery for the marker. When the store
// never moved (a crash between the marker and the move) the live store is
// already in place and only the marker goes; moving it aside would strand it.
// When it moved and no data directory exists (a crash right after the move)
// there is no partial store to keep, and moving a missing directory would stop
// the recovery; otherwise the partial store is kept aside, never deleted.
func enrolInterruptedFix(mtixDir string, marker *service.EnrolMarker) string {
	markerPath := shellQuote(filepath.Join(mtixDir, "enroll.d", "in-progress.json"))
	if !marker.Moved() {
		return "rm " + markerPath
	}
	data := filepath.Join(mtixDir, "data")
	aside := shellQuote(marker.Aside)
	if _, err := os.Lstat(data); err != nil {
		return fmt.Sprintf("mv %s %s && rm %s", aside, shellQuote(data), markerPath)
	}
	return fmt.Sprintf("mv %s %s && mv %s %s && rm %s", shellQuote(data), shellQuote(data+".kept-aside"), aside, shellQuote(data), markerPath)
}

// shellQuote quotes s for a POSIX shell: single quotes, with each embedded
// single quote closed, escaped and reopened. Nothing inside is expanded, so a
// path holding a dollar sign, a backtick or a quote is one literal word.
// Go's %q is not shell quoting: sh expands a dollar sign inside double quotes.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
