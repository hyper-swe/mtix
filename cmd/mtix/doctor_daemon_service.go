// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
)

// daemonServiceStoppedLiveID is the doctor row id of the stopped-but-running check.
const daemonServiceStoppedLiveID = "daemon.service-stopped-live"

// daemonServiceStoppedLiveFix is the doctor fix for a stopped service with a live daemon. It
// never names a pid to kill: the pid file can be stale or reused, and kill does not exist on
// every OS (MTIX-275).
const daemonServiceStoppedLiveFix = "mtix daemon stop && mtix daemon status"

// registerDaemonServiceStateCheck flags a daemon service the service manager reports stopped
// while a daemon of this project still runs (MTIX-275): the case where a restarted or foreign
// daemon keeps publishing after `mtix daemon stop`. It registers only when the platform unit
// file is installed, so projects without a service get no row. live reports the project's
// daemon pid file (daemonPIDFileLive); run is the injected service runner.
func registerDaemonServiceStateCheck(svc *service.DoctorService, spec *serviceSpec, live func() (bool, int, error), run serviceRunner) error {
	if spec == nil || spec.Path == "" {
		return nil
	}
	if _, err := os.Stat(spec.Path); err != nil {
		return nil //nolint:nilerr // no unit file means no service to compare against
	}
	return svc.Register(service.DoctorCheck{ID: daemonServiceStoppedLiveID, Run: func(ctx context.Context) service.DoctorResult {
		return daemonServiceStateRow(ctx, spec, live, run)
	}})
}

// daemonServiceStateRow compares the service manager's state with the daemon pid file.
func daemonServiceStateRow(ctx context.Context, spec *serviceSpec, live func() (bool, int, error), run serviceRunner) service.DoctorResult {
	running, err := serviceRunning(ctx, io.Discard, spec, run)
	if err != nil {
		return service.DoctorResult{Status: service.DoctorWarn,
			Detail: fmt.Sprintf("the service manager could not be asked for the daemon service state: %v", err),
			Fix:    "mtix daemon status"}
	}
	alive, pid, err := live()
	if err != nil {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: fmt.Sprintf("the daemon pid file could not be read: %v", err)}
	}
	data := map[string]any{"service_running": running, "daemon_live": alive, "daemon_pid": pid}
	if running || !alive {
		return service.DoctorResult{Status: service.DoctorPass, Data: data,
			Detail: "the service manager state matches the daemon process state"}
	}
	return service.DoctorResult{Status: service.DoctorWarn, Data: data, Fix: daemonServiceStoppedLiveFix,
		Detail: fmt.Sprintf("the service manager reports the daemon service stopped, but a daemon process of this project (pid %d) is still running and may keep publishing; if a foreground mtix daemon or mtix sync daemon is running, end it where it runs (Ctrl-C in its terminal, or close that terminal) first, then run the fix command, which stops the service and then checks it (that check exits 1 and prints stopped: the expected result after a stop). Only if the process keeps running, run mtix daemon uninstall; never end a process by pid on this advice", pid)}
}

// daemonPIDFileLiveStrict is daemonPIDFileLive for the doctor row, failing CLOSED: when the
// process identity cannot be proven (lookup error, no name, unreadable arguments or working
// directory) it returns an error, never "live", so the row warns without advice (MTIX-275).
func daemonPIDFileLiveStrict(mtixDir string) (bool, int, error) {
	return daemonPIDFileLiveStrictWith(procinfo.Info, mtixDir)
}

// daemonPIDFileLiveStrictWith is daemonPIDFileLiveStrict with an injected process lookup.
func daemonPIDFileLiveStrictWith(lookup procLookup, mtixDir string) (bool, int, error) {
	var unproven error
	probe := func(pid int) (procinfo.Proc, error) {
		proc, err := lookup(pid)
		known, _ := mtixIdentity(proc)
		switch {
		case errors.Is(err, procinfo.ErrNoProcess):
		case err != nil:
			unproven = fmt.Errorf("look up pid %d: %w", pid, err)
		case !known || len(proc.Args) == 0 || proc.Cwd == "":
			unproven = fmt.Errorf("the identity of pid %d could not be proven", pid)
		}
		return proc, err
	}
	alive, pid, err := daemonPIDFileLiveWith(probe, mtixDir)
	if err != nil {
		return false, 0, err
	}
	if alive && unproven != nil {
		return false, 0, unproven
	}
	return alive, pid, nil
}

// registerDaemonServiceStateDoctor wires the check for THIS host and project. Only an
// unsupported platform skips silently; any other spec failure surfaces as a warn row.
func registerDaemonServiceStateDoctor(svc *service.DoctorService) error {
	if app.mtixDir == "" {
		return nil
	}
	return registerDaemonServiceStateDoctorWith(svc, currentServiceSpec,
		func() (bool, int, error) { return daemonPIDFileLiveStrict(app.mtixDir) }, execServiceRunner)
}

// registerDaemonServiceStateDoctorWith is registerDaemonServiceStateDoctor over injected
// collaborators.
func registerDaemonServiceStateDoctorWith(svc *service.DoctorService, specFn func(int) (*serviceSpec, error), live func() (bool, int, error), run serviceRunner) error {
	spec, err := specFn(daemonDispatchDefaultIntervalSec)
	if errors.Is(err, errUnsupportedServicePlatform) {
		return nil // an unsupported platform has no service to check
	}
	if err != nil {
		return svc.Register(service.DoctorCheck{ID: daemonServiceStoppedLiveID, Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorWarn,
				Detail: fmt.Sprintf("the daemon service state could not be checked: %v", err)}
		}})
	}
	return registerDaemonServiceStateCheck(svc, spec, live, run)
}

// registerDaemonChecks wires the daemon doctor rows: the capture-log check and, when a
// service unit is installed, the service-state check (MTIX-275).
func registerDaemonChecks(svc *service.DoctorService) error {
	if err := registerDaemonLogsCheck(svc); err != nil {
		return fmt.Errorf("register daemon logs check: %w", err)
	}
	if err := registerDetachedListenerCheck(svc, app.mtixDir != "" && relayDir() != "", productionListenerCtl); err != nil {
		return fmt.Errorf("register detached listener diagnostics: %w", err)
	}
	return registerDaemonServiceStateDoctor(svc)
}
