// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// daemonDispatchDefaultIntervalSec is the FR-20 §4.3 dispatch cadence: the
// daemon is the origin-independent backstop, so its loop runs in seconds, not
// the old 30s pull default — a cross-machine wake should land in ~one tick.
const daemonDispatchDefaultIntervalSec = 5

// newDaemonCmd creates `mtix daemon` — the host's event dispatcher (FR-20 /
// MTIX-56.2). It supersedes `mtix sync daemon --dispatch-hooks`, which stays
// for one release as a deprecated alias.
func newDaemonCmd() *cobra.Command {
	var (
		insecureTLS bool
		intervalSec int
		install     bool
	)
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run this host's event dispatcher: pull from the hub (if configured), then fire hooks — continuously (FR-20)",
		Long: `Run the origin-independent dispatch loop: every interval (default 5s),
pull new events from the BYO Postgres hub (when one is configured) and
fire this host's hooks for every journaled event not yet dispatched
here — whoever wrote it and however it arrived (local CLI, MCP,
sync-arrival, another process). The per-(hook,event) dispatch ledger
makes firing exactly-once per host; a hook fires on every host whose
hooks.yaml configures it, so placement is designation.

With no hub configured the daemon still runs, tailing the local journal
only — cross-process writes into this .mtix keep dispatching.

Foreground process; intended for launchd/systemd supervision (see
--install). Idempotent start: a lifetime OS lock at .mtix/data/sync.daemon.lock
is shared with 'mtix sync daemon', so the two never run together.
The PID file is diagnostic and removed only by its matching owner. Transient pull errors are logged and retried, never fatal.`,
		Args: syncExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if install {
				return printDaemonInstallStub(cmd.OutOrStdout())
			}
			return runDaemon(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, intervalSec)
		},
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes only when every host the connection may use is loopback or a local socket (development only)")
	cmd.Flags().IntVar(&intervalSec, "interval", daemonDispatchDefaultIntervalSec,
		"Pull-then-dispatch interval in seconds")
	cmd.Flags().BoolVar(&install, "install", false,
		"Print a systemd unit / launchd plist stub (deprecated: use 'mtix daemon install')")
	_ = cmd.Flags().MarkDeprecated("install", "use 'mtix daemon install' (registers the OS service)")
	cmd.AddCommand(newDaemonServiceCmds()...)
	return cmd
}

// runDaemon executes the pull-then-dispatch loop until ctx is cancelled.
func runDaemon(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, intervalSec int,
) (retErr error) {
	if app.mtixDir == "" {
		return fmt.Errorf("mtix daemon: not in an mtix project (run 'mtix init' first)")
	}
	if app.store == nil {
		return fmt.Errorf("mtix daemon: local store not initialized")
	}
	if intervalSec <= 0 {
		intervalSec = daemonDispatchDefaultIntervalSec
	}

	// Hub detection: ErrDSNNotConfigured means solo/local-only — a supported
	// mode, not an error. Any OTHER failure (a DSN on the command line,
	// fail-closed tracked-config refusal, bad secrets-file permissions) must
	// abort loudly, before the daemon takes its PID file, rather than
	// silently degrade to a daemon that never pulls (FR-18.16, MTIX-95.15).
	hub := true
	if _, err := resolveSyncDSN(args); err != nil {
		if !errors.Is(err, transport.ErrDSNNotConfigured) {
			return fmt.Errorf("mtix daemon: hub DSN: %w", err)
		}
		hub = false
	}

	owner, err := startDaemonOwnership(stderr, "mtix daemon", app.mtixDir)
	if err != nil || owner == nil {
		return err
	}
	defer func() { retErr = errors.Join(retErr, owner.release()) }()

	// Mirror parity per FR-15.3 / MTIX-26.1: pulled events mutate the local
	// store, so the daemon needs the same on-commit export wiring as the MCP
	// server and serve.
	defer wireMirrorExporter(app.logger)()

	if hub {
		fmt.Fprintf(stdout, "mtix daemon: started (PID %d) — pull + dispatch every %ds\n",
			os.Getpid(), intervalSec)
	} else {
		fmt.Fprintf(stdout, "mtix daemon: started (PID %d) — no hub configured, "+
			"dispatching the local journal tail every %ds\n", os.Getpid(), intervalSec)
	}

	// This process IS the daemon trigger: allowed to dispatch even on hosts
	// whose exec-dispatch policy is "daemon" (MTIX-56.10).
	app.hooksDisp.MarkDaemon()

	// FR-21 §6.6: publish → ingest → dispatch. The relay phase sits
	// between the hub pull and dispatch so a single tick can carry an
	// event from another machine all the way to a local wake — and
	// dispatch needs no knowledge that a relay exists.
	pass := func() {
		drainPendingMirror(ctx, stderr)
		if hub {
			runOneDaemonPull(ctx, stderr, args, opts)
		}
		maybeRunRelayPhase(ctx, stderr)
		if app.hooksDisp != nil {
			app.hooksDisp.Dispatch(ctx)
		}
	}

	tick := time.NewTicker(time.Duration(intervalSec) * time.Second)
	defer tick.Stop()

	pass() // one immediate pass so a pending wake lands in seconds, not a tick
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(stdout, "mtix daemon: shutting down")
			return nil
		case <-tick.C:
			pass()
		}
	}
}
