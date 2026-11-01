// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"github.com/spf13/cobra"
)

const hostDispatcherHelp = `Experimental host dispatcher, disabled by default until setup integration ships.

One process per OS user scans locally listed checkout stores. Registration lists
a checkout without granting hub contact or user-hook execution. Only explicitly
enrolled checkouts may contact their own hub under sync.hub_contact or dispatch
user hooks. There is no public enrollment command in this build. Existing
per-project daemons and listeners keep their current behavior.

run and install require --experimental. All process-wide MTIX_SYNC_* overrides
are removed before opening state and from each checkout pull child. Live harness adapters ship with
their integrations; absent a route, messages remain available through mtix inbox.
Task Scheduler and foreground runs do not exit to self-restart after upgrades.`

func newDispatcherCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "dispatcher", Short: "Experimental per-user host dispatcher", Long: hostDispatcherHelp}
	var experimental bool
	var interval int
	var supervisor string
	run := &cobra.Command{Use: "run", Short: "Run the experimental host dispatcher in the foreground", Long: hostDispatcherHelp, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if !experimental {
			return fmt.Errorf("host dispatcher is experimental and disabled; explicit --experimental is required")
		}
		return runHostDispatcher(cmd, interval, supervisor)
	}}
	run.Flags().BoolVar(&experimental, "experimental", false, "Explicitly opt in to the unfinished host dispatcher integration")
	run.Flags().IntVar(&interval, "interval", 5, "Local scanner interval in seconds; hub contact follows sync.hub_contact")
	run.Flags().StringVar(&supervisor, "supervisor", "foreground", "Process supervision: foreground, launchd, systemd or task-scheduler")
	cmd.AddCommand(run)
	for _, verb := range []string{"install", "start", "stop", "status", "uninstall"} {
		cmd.AddCommand(newHostServiceCmd(verb))
	}
	return cmd
}

func newHostServiceCmd(verb string) *cobra.Command {
	var experimental bool
	interval := 5
	cmd := &cobra.Command{Use: verb, Short: verb + " the per-user experimental dispatcher service", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		return runHostServiceAction(cmd, verb, experimental, interval)
	}}
	if verb == "install" {
		cmd.Flags().BoolVar(&experimental, "experimental", false, "Explicitly opt in to experimental host service installation")
		cmd.Flags().IntVar(&interval, "interval", 5, "Local scanner interval in seconds")
	}
	return cmd
}

func runHostServiceAction(cmd *cobra.Command, verb string, experimental bool, interval int) error {
	if verb == "install" && !experimental {
		return fmt.Errorf("host dispatcher is experimental and disabled; explicit --experimental is required")
	}
	spec, err := currentHostServiceSpec(interval)
	if err != nil {
		return err
	}
	return applyHostServiceAction(cmd, spec, verb, runHostServiceCommand)
}

func prepareHostInstall(cmd *cobra.Command, spec *serviceSpec) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(hostLogDir(home), 0700); err != nil {
		return err
	}
	return writeServiceUnit(cmd.OutOrStdout(), spec)
}

func hostServiceArguments(spec *serviceSpec, verb string) [][]string {
	switch verb {
	case "install":
		return spec.Register
	case "start":
		return spec.Start
	case "stop":
		return spec.Stop
	case "uninstall":
		return spec.Unregister
	default:
		return spec.Status
	}
}

func finishHostServiceAction(verb string, spec *serviceSpec) error {
	var cleanupErr error
	if verb == "uninstall" && spec.Path != "" {
		if err := os.Remove(spec.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = err
		}
	}
	if verb != "install" && verb != "uninstall" {
		return nil
	}
	state, err := operatorState()
	if err != nil {
		return errors.Join(cleanupErr, err)
	}
	return errors.Join(cleanupErr, state.WriteJSON("dispatcher-service", hostServiceRecord{Installed: verb == "install"}))
}

func currentHostServiceSpec(interval int) (*serviceSpec, error) {
	exe, err := os.Executable()
	if err != nil {
		return nil, err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return buildHostServiceSpec(runtime.GOOS, stableExecutablePath(exe), home, interval)
}

func isDispatcherCommand(cmd *cobra.Command) bool {
	for c := cmd; c != nil; c = c.Parent() {
		if c.Name() == "dispatcher" && c.Parent() != nil && c.Parent().Parent() == nil {
			return true
		}
	}
	return false
}
