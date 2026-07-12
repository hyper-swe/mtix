// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// daemonPIDFilename is the path under .mtix/ where the running
// daemon's PID is recorded for idempotent start (per FR-18.18).
const daemonPIDFilename = "sync.daemon.pid"

// daemonDefaultIntervalSec is the FR-18 design default: pull every
// 30 seconds. Configurable via --interval for development.
const daemonDefaultIntervalSec = 30

// newSyncDaemonCmd creates `mtix sync daemon`. The opt-in background
// pull loop per FR-18 / MTIX-15.7.5.
//
// The daemon is a simple foreground process that pulls every N
// seconds until killed. systemd / launchd handle lifecycle (the
// '--install' subcommand generates unit files; documenting how to
// register them is a 15.12 docs concern).
//
// Idempotent start via .mtix/sync.daemon.pid: a fresh start checks
// the PID file; if a live PID is found, exits with a non-error
// message. The file is removed on graceful shutdown (SIGTERM).
func newSyncDaemonCmd() *cobra.Command {
	var (
		insecureTLS   bool
		intervalSec   int
		install       bool
		dispatchHooks bool
	)
	cmd := &cobra.Command{
		Use:   "daemon [DSN]",
		Short: "Run a background pull loop (FR-18, opt-in)",
		Long: `Run a foreground process that pulls events from the BYO Postgres
hub every N seconds (default 30) until killed. Intended for systemd
or launchd supervision; this command does NOT fork itself.

Idempotent start: a .mtix/sync.daemon.pid file marks the running
instance. If a live PID is found, this command exits with a
non-error message. The PID file is removed on graceful shutdown.

Use --install to print a systemd unit (linux) or launchd plist
(darwin) ready to be installed by the user.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if install {
				return printDaemonInstallStub(cmd.OutOrStdout())
			}
			return runSyncDaemon(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, intervalSec, dispatchHooks)
		},
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes on loopback hosts (development only)")
	cmd.Flags().IntVar(&intervalSec, "interval", daemonDefaultIntervalSec,
		"Pull interval in seconds")
	cmd.Flags().BoolVar(&install, "install", false,
		"Print a systemd unit / launchd plist for supervised install")
	cmd.Flags().BoolVar(&dispatchHooks, "dispatch-hooks", false,
		"After each pull, fire this host's hooks for undispatched events of ANY "+
			"origin (FR-20, deduped per host by the dispatch ledger). A hook fires "+
			"on every host whose hooks.yaml configures it — placement is designation")
	return cmd
}

func runSyncDaemon(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, intervalSec int, dispatchHooks bool,
) error {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix sync daemon: not in an mtix project")
	}
	if app.store == nil {
		return fmt.Errorf("mtix sync daemon: local store not initialized")
	}
	if intervalSec <= 0 {
		intervalSec = daemonDefaultIntervalSec
	}

	if held, holderPID, err := daemonPIDFileLive(app.mtixDir); err != nil {
		return wrapSyncErr(stderr, "pid file", err)
	} else if held {
		fmt.Fprintf(stderr,
			"mtix sync daemon: already running (PID %d); exiting cleanly\n", holderPID)
		return nil
	}

	if err := writeDaemonPID(app.mtixDir, os.Getpid()); err != nil {
		return wrapSyncErr(stderr, "pid file write", err)
	}
	defer removeDaemonPID(app.mtixDir)

	// Mirror parity per FR-15.3 / MTIX-26.1: pulled events mutate the
	// local store, so the daemon needs the same on-commit export wiring
	// as the MCP server and serve — its mutations must reach tasks.json
	// without a process exit.
	defer wireMirrorExporter(app.logger)()

	if dispatchHooks {
		fmt.Fprintf(stdout, "mtix sync daemon: hook dispatcher — "+
			"this host's hooks fire for events of any origin after each pull\n")
	}
	fmt.Fprintf(stdout, "mtix sync daemon: started (PID %d, interval %ds)\n",
		os.Getpid(), intervalSec)

	tick := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer tick.Stop()

	// pullThenDispatch runs one pull then one ledger dispatch pass (FR-20):
	// hooks configured on THIS host fire for the events the pull just brought
	// in — and any other undispatched events, whatever their origin — deduped
	// per (hook,event) by the dispatch ledger. Dispatch runs AFTER the pull so
	// the synced events are already in the local journal; it never blocks or
	// fails the loop.
	pullThenDispatch := func() {
		runOneDaemonPull(ctx, stderr, args, opts)
		if dispatchHooks && app.hooksDisp != nil {
			app.hooksDisp.Dispatch(ctx)
		}
	}

	// Run one immediate pull before settling into the timer cadence
	// so the user sees state move within seconds, not minutes.
	pullThenDispatch()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "mtix sync daemon: shutting down")
			return nil
		case <-tick.C:
			pullThenDispatch()
		}
	}
}

// runOneDaemonPull invokes the same logic as `mtix sync pull` once.
// Errors are logged but never returned — the daemon survives
// transient hub outages.
func runOneDaemonPull(ctx context.Context, stderr io.Writer,
	args []string, opts transport.Options,
) {
	pullCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	// Discard stdout for daemon pulls; only the parent stderr gets
	// the per-batch progress line via wrapSyncErr in pull's path.
	if err := runSyncPull(pullCtx, io.Discard, stderr, args, opts, pullDefaultBatchSize); err != nil {
		fmt.Fprintf(stderr, "mtix sync daemon: pull error (will retry): %s\n", err)
	}
}

// daemonPIDFileLive reports whether the PID file exists AND the named
// process is alive. Returns (held, pid, nil) if a live daemon owns the
// file; (false, 0, nil) if the file is absent OR the PID is stale.
func daemonPIDFileLive(mtixDir string) (bool, int, error) {
	path := filepath.Join(mtixDir, daemonPIDFilename)
	body, err := os.ReadFile(path) //nolint:gosec // path constructed from caller-supplied mtixDir
	if errors.Is(err, os.ErrNotExist) {
		return false, 0, nil
	}
	if err != nil {
		return false, 0, fmt.Errorf("read pid file: %w", err)
	}
	pid, err := strconv.Atoi(string(body))
	if err != nil {
		// Garbage in the file — treat as stale.
		_ = os.Remove(path)
		return false, 0, nil
	}
	if !pidLive(pid) {
		_ = os.Remove(path)
		return false, 0, nil
	}
	return true, pid, nil
}

// pidLive checks whether a process with the given PID is alive.
// Cross-platform: signal 0 doesn't deliver but errors if the
// process is gone.
func pidLive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal(0) is a no-op signal that returns an error iff the
	// process no longer exists. On Unix this works; on Windows
	// FindProcess always succeeds, so the signal call is the real
	// liveness probe.
	if err := proc.Signal(syscall0()); err != nil {
		return false
	}
	return true
}

// writeDaemonPID atomically writes the PID file with mode 0600.
func writeDaemonPID(mtixDir string, pid int) error {
	path := filepath.Join(mtixDir, daemonPIDFilename)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// removeDaemonPID is the deferred cleanup. Best-effort.
//
//nolint:errcheck
func removeDaemonPID(mtixDir string) {
	_ = os.Remove(filepath.Join(mtixDir, daemonPIDFilename))
}

// printDaemonInstallStub emits a minimal systemd unit / launchd plist
// stub. The user is expected to fill in paths and DSN secret refs
// before installing. v1 keeps this minimal; the workflow doc in 15.12
// fills in the per-platform install steps.
func printDaemonInstallStub(w io.Writer) error {
	fmt.Fprintln(w, "# systemd (linux) — copy to /etc/systemd/system/mtix-sync-daemon.service")
	fmt.Fprintln(w, "[Unit]")
	fmt.Fprintln(w, "Description=mtix sync daemon")
	fmt.Fprintln(w, "After=network-online.target")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "[Service]")
	fmt.Fprintln(w, "Type=simple")
	fmt.Fprintln(w, "WorkingDirectory=/path/to/your/project")
	fmt.Fprintln(w, "ExecStart=/usr/local/bin/mtix sync daemon")
	fmt.Fprintln(w, "Environment=MTIX_SYNC_DSN=<set this from a secrets manager>")
	fmt.Fprintln(w, "Restart=on-failure")
	fmt.Fprintln(w, "RestartSec=10")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "[Install]")
	fmt.Fprintln(w, "WantedBy=multi-user.target")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "# launchd (macOS) — copy to ~/Library/LaunchAgents/com.hyperswe.mtix-sync.plist")
	fmt.Fprintln(w, "# (placeholder; see https://github.com/hyper-swe/mtix/blob/main/docs/SYNC-DESIGN.md)")
	return nil
}
