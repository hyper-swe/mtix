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
	"strings"

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
		installed, _, err := legacyUnitInstalled(goos, filepath.Dir(mtixDir), home)
		return installed, err
	}
}

// uninstallLegacyService unregisters the project's per-project unit (stopping its daemon) and
// removes the unit file. A failed unregister is forgiven only when the status command proves
// the label is not loaded (spec.NotLoadedExit); any other status failure keeps the unregister
// error and the unit file. run executes service commands and is injected so tests never touch
// a real launchd or systemd unit.
func uninstallLegacyService(ctx context.Context, out io.Writer, spec *serviceSpec, run func(context.Context, io.Writer, [][]string, bool) error) error {
	if err := run(ctx, out, spec.Unregister, false); err != nil {
		// A label proven not loaded (an earlier run got this far) has nothing left to
		// unregister, so the re-run converges (a strict run, so a failed disable reaches this proof); a loaded or unknown state keeps the error.
		if !provenNotLoaded(spec, run(ctx, io.Discard, spec.Status, false)) {
			return fmt.Errorf("unregister legacy service: %w", err)
		}
	}
	if spec.Path != "" {
		if err := os.Remove(spec.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove legacy unit: %w", err)
		}
	}
	return nil
}

// provenNotLoaded reports whether a status command's error is the service manager's own
// answer that the label does not exist: the exit code the spec names. A nil error (loaded), an
// unreachable manager, a missing binary and every other exit code prove nothing.
func provenNotLoaded(spec *serviceSpec, statusErr error) bool {
	var coded interface{ ExitCode() int }
	return spec.NotLoadedExit != 0 && errors.As(statusErr, &coded) && coded.ExitCode() == spec.NotLoadedExit
}

// unreadableUnitFix is the exact inspection command for the skipped entries, and nothing else.
// The doctor Fix field must paste into a shell as it stands, so the operator-decision prose lives
// in the row Detail. Each path is single-quoted, an embedded apostrophe is rewritten as quote,
// backslash, quote, quote, and a double dash ends option parsing. Spaces, apostrophes, several
// paths and a leading dash are therefore safe.
func unreadableUnitFix(skipped []string) string {
	quoted := make([]string, len(skipped))
	for i, p := range skipped {
		quoted[i] = "'" + strings.ReplaceAll(p, "'", `'\''`) + "'"
	}
	return "ls -l -- " + strings.Join(quoted, " ")
}

// unreadableUnitDetail is the prose for the scan row; the operator decision stays out of the Fix.
const unreadableUnitDetail = "the scan for a 0.5.x per-project unit skipped entries it cannot read; no board is stopped by them. Run the fix command to inspect them; repairing or removing an entry is the operator decision, then run mtix doctor again"

// registerUnreadableUnitDoctor reports sibling unit files the scan had to skip, so a stray
// entry is visible and fixable without stopping any board (Commandment 15).
func registerUnreadableUnitDoctor(svc *service.DoctorService, skipped []string) error {
	return svc.Register(service.DoctorCheck{ID: "dispatcher.legacy-unit-scan", Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: service.DoctorWarn,
			Fix:    unreadableUnitFix(skipped),
			Detail: unreadableUnitDetail,
			Data:   map[string]any{"unreadable_units": skipped}}
	}})
}

// registerLegacyServiceDoctor flags a live 0.5.x daemon or an installed per-project unit.
// It prints the exact fix and machine-readable data for --json (Commandment 15, D11).
func registerLegacyServiceDoctor(svc *service.DoctorService, mtixDir, goos, home string) error {
	live, pid, err := daemonPIDFileLive(mtixDir)
	if err != nil {
		return fmt.Errorf("inspect legacy daemon ownership: %w", err)
	}
	unit, skipped, err := legacyUnitInstalled(goos, filepath.Dir(mtixDir), home)
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		if regErr := registerUnreadableUnitDoctor(svc, skipped); regErr != nil {
			return regErr
		}
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
