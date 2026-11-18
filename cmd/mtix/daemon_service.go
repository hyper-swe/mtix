// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

	// NotLoadedExit is the exit code of the Status command that proves the label is not
	// loaded (launchctl print: 113, systemctl status: 4); 0 when none can be proven.
	NotLoadedExit int

	// RunningMarker is a substring of the Status command's output that proves the service is
	// running ("" when a zero exit alone proves it: systemctl status exits 0 only when active).
	RunningMarker string
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
			// currently loaded) is tolerated by the runner. enable clears a
			// disabled flag left by stop, which would make bootstrap fail.
			Register: [][]string{
				{"launchctl", "bootout", target},
				{"launchctl", "enable", target},
				{"launchctl", "bootstrap", domain, path},
			},
			Unregister: [][]string{
				{"launchctl", "enable", target},
				{"launchctl", "bootout", target},
			},
			// MTIX-275: KeepAlive restarts a killed job, so stop UNLOADS it
			// (bootout) and disables it so it stays stopped across logins.
			// start re-enables, loads (a failure because it is already loaded
			// is tolerated) and restarts it.
			Start: [][]string{
				{"launchctl", "enable", target},
				{"launchctl", "bootstrap", domain, path},
				{"launchctl", "kickstart", "-k", target},
			},
			Stop: [][]string{
				{"launchctl", "disable", target},
				{"launchctl", "bootout", target},
			},
			Status: [][]string{{"launchctl", "print", target}},

			NotLoadedExit: 113,
			RunningMarker: "state = running",
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
			// MTIX-275: a manual stop is not subject to Restart=always, but
			// the enabled unit would return at the next login; disable --now
			// keeps it stopped until start (enable --now) turns it back on.
			Start:  [][]string{{"systemctl", "--user", "enable", "--now", name}},
			Stop:   [][]string{{"systemctl", "--user", "disable", "--now", name}},
			Status: [][]string{{"systemctl", "--user", "status", "--no-pager", name}},

			NotLoadedExit: 4,
		}, nil

	case "windows":
		label := "mtix-daemon-" + slug
		return &serviceSpec{
			Label: label,
			Register: [][]string{{
				"schtasks", "/Create", "/F", "/TN", label, "/SC", "ONLOGON",
				"/TR", runArgs,
			}},
			// MTIX-275: /End alone leaves the task enabled for the next
			// logon and fails when no instance runs, so stop ends the
			// instance (a failure there is tolerated) and then disables the
			// task, which fails when the task is not installed. Delete
			// ends a running instance first.
			Unregister: [][]string{
				{"schtasks", "/End", "/TN", label},
				{"schtasks", "/Delete", "/F", "/TN", label},
			},
			Start: [][]string{
				{"schtasks", "/Change", "/TN", label, "/ENABLE"},
				{"schtasks", "/Run", "/TN", label},
			},
			Stop: [][]string{
				{"schtasks", "/End", "/TN", label},
				{"schtasks", "/Change", "/TN", label, "/DISABLE"},
			},
			Status: [][]string{{"schtasks", "/Query", "/TN", label, "/FO", "CSV", "/NH"}},

			// schtasks /Query exits 1 for a task that does not exist.
			NotLoadedExit: 1,
			RunningMarker: `"Running"`,
		}, nil
	}
	return nil, fmt.Errorf("mtix daemon: %w %q", errUnsupportedServicePlatform, goos)
}

// errUnsupportedServicePlatform marks a platform without a daemon service manager.
var errUnsupportedServicePlatform = errors.New("unsupported platform")

// stableExecutablePath maps a Homebrew Cellar path to the formula's stable
// `opt` symlink (MTIX-56.11). A Cellar path is VERSION-SCOPED —
// /opt/homebrew/Cellar/mtix/1.2.3/bin/mtix — and `brew cleanup` deletes it
// after an upgrade, leaving a service registered against it silently dead
// (crash-restart respawning a missing file). Brew's /opt/<formula> symlink is
// guaranteed stable across upgrades. Invoking mtix via the usual
// /opt/homebrew/bin symlink already yields a stable path (os.Executable does
// not resolve symlinks); this guards the direct-Cellar invocation.
func stableExecutablePath(exe string) string {
	// Only the running executable's absolute path is eligible for Homebrew mapping.
	if !filepath.IsAbs(exe) {
		return exe
	}
	exe = filepath.Clean(exe)
	const marker = "/Cellar/"
	i := strings.Index(exe, marker)
	if i < 0 {
		return exe
	}
	prefix := exe[:i]                     // e.g. /opt/homebrew
	rest := exe[i+len(marker):]           // mtix/1.2.3/bin/mtix
	parts := strings.SplitN(rest, "/", 3) // [formula, version, bin/mtix]
	if len(parts) != 3 {
		return exe
	}
	stable := filepath.Join(prefix, "opt", parts[0], parts[2])
	// Resolve only for the metadata probe; retain the stable symlink as the service target.
	resolved, err := filepath.EvalSymlinks(stable)
	if err != nil || !filepath.IsAbs(resolved) {
		return exe
	}
	if _, err := os.Stat(filepath.Clean(resolved)); err != nil {
		return exe // not a real brew layout — keep what we were given
	}
	return stable
}

// projectSlug derives a short, stable, filename-safe id for a project root.
func projectSlug(projectRoot string) string {
	sum := sha256.Sum256([]byte(projectRoot))
	return hex.EncodeToString(sum[:4])
}

// newDaemonInstallCmd builds `mtix daemon install`: write the unit (if the
// platform uses one), then register — idempotent, so re-running refreshes.
func newDaemonInstallCmd(deps serviceDeps) *cobra.Command {
	var intervalSec int
	install := &cobra.Command{
		Use:   "install",
		Short: "Register the daemon as an OS service (boot-start, crash-restart)",
		Args:  syncExactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			spec, err := deps.spec(intervalSec)
			if err != nil {
				return err
			}
			if err := writeServiceUnit(cmd.OutOrStdout(), spec); err != nil {
				return fmt.Errorf("mtix daemon install: %w", err)
			}
			// Tolerate the idempotency pre-clean (e.g. bootout of a not-yet-
			// loaded label) failing; the final command must succeed.
			if err := runServiceCmdsWith(cmd.Context(), cmd.OutOrStdout(), spec.Register, true, deps.run); err != nil {
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

// currentServiceSpec builds the spec for THIS host, binary, and project.
func currentServiceSpec(intervalSec int) (*serviceSpec, error) {
	if app.mtixDir == "" {
		return nil, fmt.Errorf("mtix daemon: not in an mtix project (run 'mtix init' first)")
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("mtix daemon: resolve executable: %w", err)
	}
	exe = stableExecutablePath(exe)
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("mtix daemon: resolve home: %w", err)
	}
	return buildServiceSpec(runtime.GOOS, exe, filepath.Dir(app.mtixDir), home, intervalSec)
}

// serviceRunner executes one service-manager command, writing its output to out. It is
// injected so tests never touch a real launchd, systemd or Task Scheduler.
type serviceRunner func(ctx context.Context, out io.Writer, argv []string) error

// execServiceRunner runs argv as a real process.
func execServiceRunner(ctx context.Context, out io.Writer, argv []string) error {
	c := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv is built from constants + local paths above, never user input
	c.Stdout = out
	c.Stderr = out
	return c.Run()
}

// runServiceCmds executes each command for real; see runServiceCmdsWith.
func runServiceCmds(ctx context.Context, out io.Writer, cmds [][]string, lenient bool) error {
	return runServiceCmdsWith(ctx, out, cmds, lenient, execServiceRunner)
}

// runServiceCmdsWith executes each command, echoing it. With lenient=true every
// command but the LAST may fail (idempotency pre-cleans like bootout of an
// unloaded label); the last command's error is always surfaced.
func runServiceCmdsWith(ctx context.Context, out io.Writer, cmds [][]string, lenient bool, run serviceRunner) error {
	for i, argv := range cmds {
		fmt.Fprintf(out, "$ %s\n", strings.Join(argv, " "))
		if err := run(ctx, out, argv); err != nil {
			if lenient && i < len(cmds)-1 {
				continue
			}
			return err
		}
	}
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
