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
	"path"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
)

// legacyServiceFix is the one command that migrates a legacy service (run inside the project).
const legacyServiceFix = "mtix setup"

// pidIsMtixProcess reports whether pid names an mtix executable. A pid file whose pid was
// reused by an unrelated process is not a live legacy service. Identity that cannot be read
// fails safe as "is mtix": skipping duty is recoverable, a double pull or hook firing is not.
func pidIsMtixProcess(pid int) bool {
	proc, err := procinfo.Info(pid)
	if errors.Is(err, procinfo.ErrNoProcess) {
		return false
	}
	if err != nil {
		return true
	}
	known := false
	for _, name := range []string{proc.Name, proc.Argv0, proc.Exe} {
		if name == "" {
			continue
		}
		known = true
		base := strings.ToLower(path.Base(strings.ReplaceAll(name, `\`, "/")))
		if strings.HasPrefix(base, "mtix") {
			return true
		}
	}
	return !known
}

// legacyServiceLive is the host dispatcher's probe: a read-only look at the PID file that
// never writes it and never takes the daemon's lifetime lock (D11, FR-20.4).
func legacyServiceLive(_ context.Context, mtixDir string) (bool, error) {
	live, _, err := daemonPIDFileLive(mtixDir)
	return live, err
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
