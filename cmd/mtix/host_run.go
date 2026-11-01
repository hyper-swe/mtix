// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func runHostDispatcher(cmd *cobra.Command, interval int, supervisor string) (retErr error) {
	if err := clearHostSyncEnvironment(); err != nil {
		return err
	}
	if interval <= 0 {
		return fmt.Errorf("dispatcher interval must be positive")
	}
	policy, err := hostSupervisor(supervisor)
	if err != nil {
		return err
	}
	st, err := operatorState()
	if err != nil {
		return err
	}
	lock, err := st.AcquireDispatcher(cmd.Context())
	if err != nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, lock.Close()) }()
	return runHostPasses(cmd, st, policy, interval)
}

func runHostPasses(cmd *cobra.Command, st *oplocal.State, policy attach.Supervisor, interval int) error {
	host, uid, err := registrationHostIdentity()
	if err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	baseline, err := currentHostTarget(st)
	if err != nil {
		return err
	}
	dispatcher, err := service.NewHostDispatcher(service.HostDispatcherOptions{State: st, OpenBoard: func(ctx context.Context, row oplocal.ListedBoard) (*service.HostBoard, error) {
		return openHostBoard(ctx, row, st, service.RegistrationHost{HostID: host, UID: uid, Version: version, Protocol: attach.Protocol}, exe)
	}})
	if err != nil {
		return err
	}
	tick := time.NewTicker(time.Duration(interval) * time.Second)
	defer tick.Stop()
	for {
		if err := dispatcher.Pass(cmd.Context()); err != nil {
			slog.Warn("host dispatcher pass failed", "error", err)
		}
		if checkHostTarget(cmd.Context(), st, policy, baseline) {
			return nil
		}
		select {
		case <-cmd.Context().Done():
			return nil
		case <-tick.C:
		}
	}
}

func hostSupervisor(name string) (attach.Supervisor, error) {
	switch name {
	case "foreground":
		return attach.SupervisorForeground, nil
	case "launchd":
		return attach.SupervisorKeepAlive, nil
	case "systemd":
		return attach.SupervisorRestartAlways, nil
	case "task-scheduler":
		return attach.SupervisorBestEffort, nil
	}
	return 0, fmt.Errorf("unknown dispatcher supervisor %q", name)
}

func checkHostTarget(ctx context.Context, st *oplocal.State, supervisor attach.Supervisor, baseline hostTargetSnapshot) bool {
	target, physical, initial := baseline.target, baseline.physical, baseline.initial
	resolved, err := attach.ValidateShimTarget(target, os.Getuid())
	changed := resolved != physical
	if err == nil {
		info, statErr := os.Stat(resolved)
		err = statErr
		if err == nil {
			changed = changed || !os.SameFile(initial, info) || !initial.ModTime().Equal(info.ModTime()) || initial.Size() != info.Size()
		}
	}
	decision := attach.DecideSelfRestart(attach.SelfRestartInput{Supervisor: supervisor, TargetChanged: changed || err != nil, TargetValid: err == nil})
	if decision.Alert {
		_, alertErr := service.NewAlerts(st, utcNow).Raise(ctx, service.AlertRequest{Agent: "dispatcher", Class: "wake.version-skew", Detail: decision.Reason, Fix: "mtix dispatcher stop; mtix dispatcher start"})
		if alertErr != nil {
			slog.Warn("dispatcher skew alert failed", "error", alertErr)
		}
	}
	return decision.Exit
}

func openHostBoard(_ context.Context, row oplocal.ListedBoard, st *oplocal.State, host service.RegistrationHost, exe string) (*service.HostBoard, error) {
	if err := listedHostBoard(row); err != nil {
		return nil, err
	}
	dir := filepath.Join(row.Path, ".mtix")
	store, err := sqlite.New(filepath.Join(dir, "data", "mtix.db"), slog.Default())
	if err != nil {
		return nil, err
	}
	source, err := newHubContactSource(dir)
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	board, err := service.NewHostBoard(service.HostBoardOptions{Store: store, State: st, Path: row.Path, Host: host, PID: int64(os.Getpid()), Clock: utcNow, Logger: slog.Default(), Close: store.Close, Contact: service.NewContactScheduler(utcNow, source.snapshot), Pull: func(ctx context.Context, dsn string) error { return pullHostCheckout(ctx, exe, row.Path, dsn) }, LegacyService: legacyServiceLive})
	if err != nil {
		return nil, errors.Join(err, store.Close())
	}
	return board, nil
}

// pullHostCheckout delegates the existing full pull engine in a separate process.
// Its one DSN is resolved from the enrolled checkout file by the parent; no inherited DSN survives.
func pullHostCheckout(ctx context.Context, exe, root, dsn string) error {
	child := exec.CommandContext(ctx, exe, "sync", "pull") // #nosec G204 -- fixed argv and the running executable, never repository commands
	child.Dir = root
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !hostSyncOverride(name) && !strings.HasPrefix(entry, "MTIX_AUTHOR_ID=") {
			child.Env = append(child.Env, entry)
		}
	}
	child.Env = append(child.Env, "MTIX_SYNC_DSN="+dsn)
	if err := child.Run(); err != nil {
		return fmt.Errorf("enrolled checkout pull failed: %w", err)
	}
	return nil
}

type hostTargetSnapshot struct {
	target, physical string
	initial          os.FileInfo
}

func currentHostTarget(st *oplocal.State) (hostTargetSnapshot, error) {
	var out hostTargetSnapshot
	exe, err := os.Executable()
	if err != nil {
		return out, err
	}
	out.target = stableExecutablePath(exe)
	var shim attach.ShimRecord
	reports, err := st.ReadJSON("attach-shim", &shim)
	if err != nil {
		return out, err
	}
	if len(reports) == 0 && shim.Target != "" {
		out.target = shim.Target
	}
	out.physical, err = attach.ValidateShimTarget(out.target, os.Getuid())
	if err != nil {
		return out, err
	}
	out.initial, err = os.Stat(out.physical)
	return out, err
}

// clearHostSyncEnvironment removes every exported sync override before any checkout is opened.
func clearHostSyncEnvironment() error {
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if hostSyncOverride(name) {
			if err := os.Unsetenv(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// hostSyncOverride also matches Windows environment names, which are case-insensitive.
func hostSyncOverride(name string) bool {
	return strings.HasPrefix(strings.ToUpper(name), "MTIX_SYNC_")
}
