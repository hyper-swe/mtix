// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

// errDaemonServiceNotRunning is returned by `mtix daemon status` when the service is stopped
// or not installed, so scripts and recovery steps can rely on the exit code (MTIX-275).
var errDaemonServiceNotRunning = errors.New("daemon service is not running")

// serviceDeps carries the injected collaborators of the daemon service verbs.
type serviceDeps struct {
	run  serviceRunner
	spec func(intervalSec int) (*serviceSpec, error)
}

// bestEffortServiceCmd reports whether argv is an idempotency pre-clean whose failure carries no
// information: ending a task instance that is not running (schtasks /End). Every other command
// is judged on its own result (MTIX-275).
func bestEffortServiceCmd(argv []string) bool {
	return len(argv) >= 2 && argv[0] == "schtasks" && strings.EqualFold(argv[1], "/End")
}

// runForgivingNotLoaded runs cmds in order. A failure is forgiven only for a best-effort
// pre-clean, or when the Status command proves the service is not loaded (the idempotent
// "nothing to stop or remove" case); a loaded or unknown state returns the failing command's
// error at once, so the caller never reports success for a service that may still run (MTIX-275).
func runForgivingNotLoaded(ctx context.Context, out io.Writer, spec *serviceSpec, cmds [][]string, run serviceRunner) error {
	forgiven := false
	for _, argv := range cmds {
		fmt.Fprintf(out, "$ %s\n", strings.Join(argv, " "))
		err := run(ctx, out, argv)
		if err == nil || bestEffortServiceCmd(argv) {
			continue
		}
		if len(spec.Status) == 0 || !provenNotLoaded(spec, run(ctx, io.Discard, spec.Status[0])) {
			return err
		}
		forgiven = true
	}
	if forgiven {
		fmt.Fprintln(out, "the daemon service is not loaded; nothing to do")
	}
	return nil
}

// stopDaemonService unloads or disables the service so it stays stopped until start (MTIX-275).
func stopDaemonService(ctx context.Context, out io.Writer, spec *serviceSpec, run serviceRunner) error {
	return runForgivingNotLoaded(ctx, out, spec, spec.Stop, run)
}

// startDaemonService re-enables, loads and starts the service (MTIX-275).
func startDaemonService(ctx context.Context, out io.Writer, spec *serviceSpec, run serviceRunner) error {
	return runServiceCmdsWith(ctx, out, spec.Start, true, run)
}

// uninstallDaemonService ends and unregisters the service, then removes its unit file. It is
// lenient when nothing is installed and fails on any other error (MTIX-275).
func uninstallDaemonService(ctx context.Context, out io.Writer, spec *serviceSpec, run serviceRunner) error {
	if err := runForgivingNotLoaded(ctx, out, spec, spec.Unregister, run); err != nil {
		return err
	}
	if spec.Path == "" {
		return nil
	}
	if err := os.Remove(spec.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove unit file: %w", err)
	}
	fmt.Fprintf(out, "removed %s\n", spec.Path)
	return nil
}

// serviceRunning reports whether the service manager says the service is running. A command
// that ran and answered not-running (a coded exit) is (false, nil); a manager that cannot be
// reached or a missing binary is an error.
func serviceRunning(ctx context.Context, out io.Writer, spec *serviceSpec, run serviceRunner) (bool, error) {
	var buf bytes.Buffer
	err := run(ctx, &buf, spec.Status[0])
	if out != nil {
		_, _ = out.Write(buf.Bytes())
	}
	if err == nil {
		return spec.RunningMarker == "" || strings.Contains(buf.String(), spec.RunningMarker), nil
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) {
		return false, nil
	}
	return false, err
}

// statusDaemonService prints the service state and fails with errDaemonServiceNotRunning when
// it is stopped or not installed (MTIX-275).
func statusDaemonService(ctx context.Context, out io.Writer, spec *serviceSpec, run serviceRunner) error {
	fmt.Fprintf(out, "$ %s\n", strings.Join(spec.Status[0], " "))
	running, err := serviceRunning(ctx, out, spec, run)
	if err != nil {
		return err
	}
	if !running {
		fmt.Fprintln(out, "stopped (not running, or no service is installed)")
		return errDaemonServiceNotRunning
	}
	fmt.Fprintln(out, "running")
	return nil
}

// newDaemonServiceCmds returns the service-lifecycle subcommands for `mtix daemon`.
func newDaemonServiceCmds() []*cobra.Command {
	return newDaemonServiceCmdsWith(serviceDeps{run: execServiceRunner, spec: currentServiceSpec})
}

// newDaemonServiceCmdsWith builds the verbs over injected dependencies.
func newDaemonServiceCmdsWith(deps serviceDeps) []*cobra.Command {
	verb := func(use, short string, do func(context.Context, io.Writer, *serviceSpec, serviceRunner) error) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  syncExactArgs(0),
			RunE: func(cmd *cobra.Command, _ []string) error {
				spec, err := deps.spec(daemonDispatchDefaultIntervalSec)
				if err != nil {
					return err
				}
				if err := do(cmd.Context(), cmd.OutOrStdout(), spec, deps.run); err != nil {
					return fmt.Errorf("mtix daemon %s: %w", use, err)
				}
				return nil
			},
		}
	}
	return []*cobra.Command{
		newDaemonInstallCmd(deps),
		verb("uninstall", "End and unregister the daemon service and remove its unit file", uninstallDaemonService),
		verb("start", "Start the installed daemon service (re-enables it after stop)", startDaemonService),
		verb("stop", "Stop the daemon service; it stays stopped until start", stopDaemonService),
		verb("status", "Show the installed daemon service's state; fails when stopped", statusDaemonService),
	}
}
