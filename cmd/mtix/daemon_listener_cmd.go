// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/procinfo"
)

// addListenerFlags gives `mtix daemon stop` and `mtix daemon status` the
// --listener mode (MTIX-276): the same verbs, aimed at the detached
// `mtix daemon --listen` a waiter starts. Without --listener each verb keeps
// its service behavior unchanged.
func addListenerFlags(deps serviceDeps, stop, status *cobra.Command) {
	factory := deps.listener
	if factory == nil {
		factory = productionListenerCtl
	}
	serviceStop, serviceStatus := stop.RunE, status.RunE
	stop.Flags().Bool("listener", false, "Act on the detached inbox listener (mtix daemon --listen) instead of the OS service; exits 0 stopped, 9 a live waiter started a new listener, 11 still running or not verified, 12 only a non-listener daemon holds the lock")
	stop.Flags().Bool("json", false, "With --listener: print the result as JSON")
	stop.Flags().Duration("timeout", listenerStopTimeout, "With --listener: how long to wait for the listener to exit before escalating")
	status.Flags().Bool("listener", false, "Show the detached inbox listener (pid, waiters) instead of the OS service; exits 0 when running, 10 when the lock is free and nothing runs (1 is an error), 12 when a non-listener daemon holds the lock, 8 when the lock holder is not a verified listener")
	status.Flags().Bool("json", false, "With --listener: print the result as JSON")
	stop.RunE = func(cmd *cobra.Command, args []string) error {
		listener, err := listenerMode(cmd, "timeout")
		if err != nil || !listener {
			if err != nil {
				return err
			}
			return serviceStop(cmd, args)
		}
		timeout, _ := cmd.Flags().GetDuration("timeout")
		if timeout <= 0 {
			return errors.New("mtix daemon stop: --timeout must be positive")
		}
		ctl, err := factory()
		if err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		return ctl.stop(cmd.OutOrStdout(), listenerStopOpts{timeout: timeout}, asJSON)
	}
	status.RunE = func(cmd *cobra.Command, args []string) error {
		listener, err := listenerMode(cmd)
		if err != nil || !listener {
			if err != nil {
				return err
			}
			return serviceStatus(cmd, args)
		}
		ctl, err := factory()
		if err != nil {
			return err
		}
		asJSON, _ := cmd.Flags().GetBool("json")
		return ctl.status(cmd.OutOrStdout(), asJSON)
	}
}

// listenerMode reports whether --listener is set and refuses the listener-only
// flags (--json and the named extras) without it, so a typo never silently
// runs the service verb.
func listenerMode(cmd *cobra.Command, extras ...string) (bool, error) {
	listener, _ := cmd.Flags().GetBool("listener")
	if listener {
		return true, nil
	}
	for _, name := range append([]string{"json"}, extras...) {
		if cmd.Flags().Changed(name) {
			return false, fmt.Errorf("mtix daemon %s: --%s needs --listener", cmd.Name(), name)
		}
	}
	return false, nil
}

// productionListenerCtl wires the listener control to this project's real
// lock, pid file, process table, waiter leases and stop request file.
func productionListenerCtl() (*listenerCtl, error) {
	if app.mtixDir == "" {
		return nil, errors.New("mtix daemon: not in an mtix project (run 'mtix init' first)")
	}
	src, err := newHubContactSource(app.mtixDir)
	if err != nil {
		return nil, fmt.Errorf("mtix daemon: %w", err)
	}
	dir := app.mtixDir
	return &listenerCtl{
		goos:       runtime.GOOS,
		projectDir: filepath.Dir(src.mtixDir),
		lockHeld:   func() (bool, error) { return daemonLockHeld(dir) },
		readPID:    func() (int, error) { return readDaemonPID(dir) },
		info:       procinfo.Info,
		leases:     src.liveLeaseList,
		signal:     signalTerm,
		writeStop:  func() error { return writeListenerStopRequest(dir) },
		clearStop:  func() error { return clearListenerStopRequest(dir) },
		now:        utcNow,
		sleep:      time.Sleep,
	}, nil
}

// signalTerm sends SIGTERM to pid. It is only reached on unix, after the
// listener's identity was verified twice (listenerCtl.escalate).
func signalTerm(pid int) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	if err := proc.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("signal process %d: %w", pid, err)
	}
	return nil
}
