// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"path/filepath"

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
		data := filepath.Join(mtixDir, "data")
		return service.DoctorResult{
			Status: service.DoctorFail,
			Detail: "a worktree enrolment was interrupted; " + marker.RecoveryHint(),
			Fix: fmt.Sprintf("mv %q %q && mv %q %q && rm %q",
				data, data+".kept-aside", marker.Aside, data, filepath.Join(mtixDir, "enroll.d", "in-progress.json")),
			Data: map[string]any{"aside_dir": marker.Aside, "backup_path": marker.Backup, "started_at": marker.Started, "pid": marker.PID},
		}
	}})
}
