// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/cobra"
)

// MTIX-56.3: the daemon is INFRASTRUCTURE (FR-20 G3) — an OS-managed service
// that starts on boot and restarts on crash, not a command a human remembers
// to run. `mtix daemon install|status|start|stop|uninstall` generates and
// registers the platform service:
//
//   darwin   a per-user launchd agent (~/Library/LaunchAgents), KeepAlive so
//            launchd restarts it on crash, RunAtLoad for login-start
//   linux    a systemd --user unit (~/.config/systemd/user), Restart=always,
//            enabled --now
//   windows  a Task Scheduler logon task (schtasks); crash-restart is
//            best-effort there — the daemon's own PID lock keeps re-runs safe
//
// One service per PROJECT (the label carries a hash of the project root), so
// several projects can each run their dispatcher. Install is idempotent:
// re-running overwrites the unit and re-registers.

// serviceSpec is the pure, testable description of the platform service: the
// unit file (if any) plus the exact commands each verb runs. Building it does
// not touch the system.
type serviceSpec struct {
	Label   string
	Path    string // unit/plist file path ("" when the platform needs none)
	Content string // unit/plist body

	Register   [][]string
	Unregister [][]string
	Start      [][]string
	Stop       [][]string
	Status     [][]string
}

// buildServiceSpec assembles the platform service for the project rooted at
// projectRoot (the directory containing .mtix), running exe as the daemon.
func buildServiceSpec(goos, exe, projectRoot, home string, intervalSec int) (*serviceSpec, error) {
	slug := projectSlug(projectRoot)
	runArgs := fmt.Sprintf("%s -C %s daemon --interval %d", exe, projectRoot, intervalSec)
	logDir := filepath.Join(projectRoot, ".mtix", "logs")

	switch goos {
	case "darwin":
		label := "com.hyperswe.mtix.daemon." + slug
		path := filepath.Join(home, "Library", "LaunchAgents", label+".plist")
		content := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key><string>%s</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>-C</string>
        <string>%s</string>
        <string>daemon</string>
        <string>--interval</string>
        <string>%d</string>
    </array>
    <key>RunAtLoad</key><true/>
    <key>KeepAlive</key><true/>
    <key>StandardOutPath</key><string>%s</string>
    <key>StandardErrorPath</key><string>%s</string>
</dict>
</plist>
`, label, exe, projectRoot, intervalSec,
			filepath.Join(logDir, "daemon.out.log"), filepath.Join(logDir, "daemon.err.log"))
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
		domain := fmt.Sprintf("gui/%d", os.Getuid())
		return &serviceSpec{
			Label: label, Path: path, Content: content,
			// bootout-first makes re-install idempotent; its failure (not
			// currently loaded) is tolerated by the runner.
			Register: [][]string{
				{"launchctl", "bootout", target},
				{"launchctl", "bootstrap", domain, path},
			},
			Unregister: [][]string{{"launchctl", "bootout", target}},
			Start:      [][]string{{"launchctl", "kickstart", "-k", target}},
			Stop:       [][]string{{"launchctl", "kill", "SIGTERM", target}},
			Status:     [][]string{{"launchctl", "print", target}},
		}, nil

	case "linux":
		name := "mtix-daemon-" + slug + ".service"
		path := filepath.Join(home, ".config", "systemd", "user", name)
		content := fmt.Sprintf(`[Unit]
Description=mtix event dispatcher daemon (%s)

[Service]
ExecStart=%s
Restart=always
RestartSec=2

[Install]
WantedBy=default.target
`, projectRoot, runArgs)
		return &serviceSpec{
			Label: name, Path: path, Content: content,
			Register: [][]string{
				{"systemctl", "--user", "daemon-reload"},
				{"systemctl", "--user", "enable", "--now", name},
			},
			Unregister: [][]string{
				{"systemctl", "--user", "disable", "--now", name},
				{"systemctl", "--user", "daemon-reload"},
			},
			Start:  [][]string{{"systemctl", "--user", "start", name}},
			Stop:   [][]string{{"systemctl", "--user", "stop", name}},
			Status: [][]string{{"systemctl", "--user", "status", "--no-pager", name}},
		}, nil

	case "windows":
		label := "mtix-daemon-" + slug
		return &serviceSpec{
			Label: label,
			Register: [][]string{{
				"schtasks", "/Create", "/F", "/TN", label, "/SC", "ONLOGON",
				"/TR", runArgs,
			}},
			Unregister: [][]string{{"schtasks", "/Delete", "/F", "/TN", label}},
			Start:      [][]string{{"schtasks", "/Run", "/TN", label}},
			Stop:       [][]string{{"schtasks", "/End", "/TN", label}},
			Status:     [][]string{{"schtasks", "/Query", "/TN", label}},
		}, nil
	}
	return nil, fmt.Errorf("mtix daemon: unsupported platform %q", goos)
}

// projectSlug derives a short, stable, filename-safe id for a project root.
func projectSlug(projectRoot string) string {
	sum := sha256.Sum256([]byte(projectRoot))
	return hex.EncodeToString(sum[:4])
}

// newDaemonInstallCmd builds `mtix daemon install`: write the unit (if the
// platform uses one), then register — idempotent, so re-running refreshes.
func newDaemonInstallCmd() *cobra.Command {
	var intervalSec int
	install := &cobra.Command{
		Use:   "install",
		Short: "Register the daemon as an OS service (boot-start, crash-restart)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			spec, err := currentServiceSpec(intervalSec)
			if err != nil {
				return err
			}
			if err := writeServiceUnit(cmd.OutOrStdout(), spec); err != nil {
				return fmt.Errorf("mtix daemon install: %w", err)
			}
			// Tolerate the idempotency pre-clean (e.g. bootout of a not-yet-
			// loaded label) failing; the final command must succeed.
			if err := runServiceCmds(cmd.Context(), cmd.OutOrStdout(), spec.Register, true); err != nil {
				return fmt.Errorf("mtix daemon install: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s — boot-start + crash-restart active\n", spec.Label)
			fmt.Fprintln(cmd.OutOrStdout(), "upgrading later? replace the binary with unlink-then-copy (install/rm+cp/mv, never cp over it), then 'mtix daemon start'")
			return nil
		},
	}
	install.Flags().IntVar(&intervalSec, "interval", daemonDispatchDefaultIntervalSec,
		"Pull-then-dispatch interval in seconds baked into the service")
	return install
}

// writeServiceUnit persists the unit/plist file for platforms that need one.
func writeServiceUnit(out io.Writer, spec *serviceSpec) error {
	if spec.Path == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(spec.Path), 0o755); err != nil { //nolint:gosec // service dirs are user-visible
		return err
	}
	if err := os.WriteFile(spec.Path, []byte(spec.Content), 0o644); err != nil { //nolint:gosec // unit files are world-readable by convention
		return err
	}
	fmt.Fprintf(out, "wrote %s\n", spec.Path)
	return nil
}

// newDaemonServiceCmds returns the service-lifecycle subcommands for `mtix
// daemon`.
func newDaemonServiceCmds() []*cobra.Command {
	verb := func(use, short string, pick func(*serviceSpec) [][]string, lenient bool) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  cobra.NoArgs,
			RunE: func(cmd *cobra.Command, _ []string) error {
				spec, err := currentServiceSpec(daemonDispatchDefaultIntervalSec)
				if err != nil {
					return err
				}
				if err := runServiceCmds(cmd.Context(), cmd.OutOrStdout(), pick(spec), lenient); err != nil {
					return fmt.Errorf("mtix daemon %s: %w", use, err)
				}
				return nil
			},
		}
	}

	uninstall := verb("uninstall", "Unregister the daemon service and remove its unit file",
		func(s *serviceSpec) [][]string { return s.Unregister }, true)
	uninstall.PostRunE = func(cmd *cobra.Command, _ []string) error {
		spec, err := currentServiceSpec(daemonDispatchDefaultIntervalSec)
		if err != nil {
			return err
		}
		if spec.Path != "" {
			if err := os.Remove(spec.Path); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "removed %s\n", spec.Path)
		}
		return nil
	}

	return []*cobra.Command{
		newDaemonInstallCmd(),
		uninstall,
		verb("start", "Start the installed daemon service", func(s *serviceSpec) [][]string { return s.Start }, false),
		verb("stop", "Stop the installed daemon service", func(s *serviceSpec) [][]string { return s.Stop }, false),
		verb("status", "Show the installed daemon service's state", func(s *serviceSpec) [][]string { return s.Status }, false),
	}
}

// currentServiceSpec builds the spec for THIS host, binary, and project.
func currentServiceSpec(intervalSec int) (*serviceSpec, error) {
	if app.mtixDir == "" {
		return nil, fmt.Errorf("mtix daemon: not in an mtix project (run 'mtix init' first)")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("mtix daemon: resolve executable: %w", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("mtix daemon: resolve home: %w", err)
	}
	return buildServiceSpec(runtime.GOOS, exe, filepath.Dir(app.mtixDir), home, intervalSec)
}

// runServiceCmds executes each command, echoing it. With lenient=true every
// command but the LAST may fail (idempotency pre-cleans like bootout of an
// unloaded label); the last command's error is always surfaced.
func runServiceCmds(ctx context.Context, out io.Writer, cmds [][]string, lenient bool) error {
	for i, argv := range cmds {
		fmt.Fprintf(out, "$ %s\n", strings.Join(argv, " "))
		c := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is built from constants + local paths above, never user input
		c.Stdout = out
		c.Stderr = out
		if err := c.Run(); err != nil {
			if lenient && i < len(cmds)-1 {
				continue
			}
			return err
		}
	}
	return nil
}
