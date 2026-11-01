// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Legacy 0.5.x per-project service coexistence (ADR-007 D11, MTIX-112.17.15).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/service"
)

// legacyServiceFix is doctor's fix: the existing command, run inside the project, that
// unregisters the per-project service and removes its unit. It names only commands this
// build ships; the migration command that enrolls the board ships in a later build.
const legacyServiceFix = "mtix daemon uninstall"

// legacyServiceProbe is the host dispatcher's read-only duty probe: a 0.5.x per-project
// service owns pull and hook duty while its daemon is live OR its unit is installed, so the
// crash-restart gap of a supervised unit never lets the host fire next to it. It never writes
// the PID file and never takes the daemon's lifetime lock (D11, FR-20.4).
func legacyServiceProbe(goos string, homeDir func() (string, error)) func(context.Context, string) (bool, error) {
	return func(_ context.Context, mtixDir string) (bool, error) {
		live, _, err := daemonPIDFileLive(mtixDir)
		if err != nil || live {
			return live, err
		}
		home, err := homeDir()
		if err != nil {
			return false, fmt.Errorf("resolve home for legacy unit check: %w", err)
		}
		return legacyUnitInstalled(goos, filepath.Dir(mtixDir), home)
	}
}

// legacyUnitInstalled reports whether the per-project unit for projectRoot exists on disk.
// Platforms without a unit file (Task Scheduler) report false; doctor then relies on the PID file.
func legacyUnitInstalled(goos, projectRoot, home string) (bool, error) {
	spec, err := buildServiceSpec(goos, "mtix", projectRoot, home, daemonDispatchDefaultIntervalSec)
	if err != nil || spec.Path == "" {
		return false, err
	}
	if _, statErr := os.Lstat(spec.Path); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("inspect legacy unit: %w", statErr)
	}
	return true, nil
}

// uninstallLegacyService unregisters the project's per-project unit (stopping its daemon) and
// removes the unit file. run executes service commands and is injected so tests never touch a
// real launchd or systemd unit.
func uninstallLegacyService(ctx context.Context, out io.Writer, spec *serviceSpec, run func(context.Context, io.Writer, [][]string, bool) error) error {
	if err := run(ctx, out, spec.Unregister, true); err != nil {
		return fmt.Errorf("unregister legacy service: %w", err)
	}
	if spec.Path != "" {
		if err := os.Remove(spec.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy unit: %w", err)
		}
	}
	return nil
}

// registerLegacyServiceDoctor flags a live 0.5.x daemon or an installed per-project unit.
// It prints the exact fix and machine-readable data for --json (Commandment 15, D11).
func registerLegacyServiceDoctor(svc *service.DoctorService, mtixDir, goos, home string) error {
	live, pid, err := daemonPIDFileLive(mtixDir)
	if err != nil {
		return fmt.Errorf("inspect legacy daemon ownership: %w", err)
	}
	unit, err := legacyUnitInstalled(goos, filepath.Dir(mtixDir), home)
	if err != nil {
		return err
	}
	if !live && !unit {
		return nil
	}
	return svc.Register(service.DoctorCheck{ID: "dispatcher.legacy-service", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn, Fix: legacyServiceFix,
			Detail: "a 0.5.x per-project service still owns pull and hook duty for this board; the host dispatcher runs only the wake scanner for it until it is migrated",
			Data:   map[string]any{"daemon_live": live, "daemon_pid": pid, "unit_installed": unit}}
	}})
}
