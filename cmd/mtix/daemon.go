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

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
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
		listen      bool
	)
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Run this host's event dispatcher: pull from the hub (if configured), then fire hooks — continuously (FR-20)",
		Long: `Run the origin-independent dispatch loop. Local work (pending-export
recovery, relay, hook dispatch) runs every interval (default 5s); the hub is
contacted only when sync.hub_contact says so: activity (default: only for
30 minutes after real activity), interval:<duration>, or manual (never). While
sync.listen is live and a waiter (mtix inbox --wait) holds a lease, the hub is also contacted every
sync.listen_interval until sync.listen_max_idle after the last activity. Each
contact pulls new events from the BYO Postgres hub (when one is configured) and
fires this host's hooks for every journaled event not yet dispatched
here — whoever wrote it and however it arrived (local CLI, MCP,
sync-arrival, another process). The per-(hook,event) dispatch ledger
makes firing exactly-once per host; a hook fires on every host whose
hooks.yaml configures it, so placement is designation.

With no hub configured the daemon still runs, tailing the local journal
only — cross-process writes into this .mtix keep dispatching.

Foreground process; intended for launchd/systemd supervision (see
--install). Idempotent start: a lifetime OS lock at .mtix/data/sync.daemon.lock
is shared with 'mtix sync daemon', so the two never run together.
The PID file is diagnostic and removed only by its matching owner. Each pull/relay/dispatch pass takes
.mtix/data/dispatch.lock; contention idles and retries at the existing cadence.
Push and dispatch lock files and their data directory must be real and user-owned. Transient pull errors are logged and retried, never fatal.`,
		Args: syncExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if install {
				return printDaemonInstallStub(cmd.OutOrStdout())
			}
			return runDaemon(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(),
				args, transport.Options{InsecureTLS: insecureTLS}, intervalSec, daemonPassControl{listen: listen})
		},
	}
	cmd.Flags().BoolVar(&insecureTLS, "insecure-tls", false,
		"Allow weaker TLS modes only when every host the connection may use is loopback or a local socket (development only)")
	cmd.Flags().IntVar(&intervalSec, "interval", daemonDispatchDefaultIntervalSec,
		"Pull-then-dispatch interval in seconds")
	cmd.Flags().BoolVar(&listen, "listen", false,
		"Listener mode: pull once, serve while a waiter lease is live, exit when the last lease ends (started by mtix inbox --wait)")
	cmd.Flags().BoolVar(&install, "install", false,
		"Print a systemd unit / launchd plist stub (deprecated: use 'mtix daemon install')")
	_ = cmd.Flags().MarkDeprecated("install", "use 'mtix daemon install' (registers the OS service)")
	cmd.AddCommand(newDaemonServiceCmds()...)
	return cmd
}

// runDaemon executes the pull-then-dispatch loop until ctx is cancelled.
func runDaemon(ctx context.Context, stdout, stderr io.Writer,
	args []string, opts transport.Options, intervalSec int, control ...daemonPassControl,
) (retErr error) {
	hub, err := daemonPreflight(args)
	if err != nil {
		return err
	}
	if intervalSec <= 0 {
		intervalSec = daemonDispatchDefaultIntervalSec
	}

	listen := len(control) > 0 && control[0].listen
	owner, err := daemonOwnershipFor(stderr, app.mtixDir)
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
	sched, onContact, err := newDaemonScheduler(stdout, stderr, hub, intervalSec)
	if err != nil {
		return err
	}
	pass := buildDaemonPass(hub, daemonPassSteps{
		drain: func() { drainPendingMirror(ctx, stderr) },
		pull:  func() { runOneDaemonPull(ctx, stderr, args, opts) },
		relay: func() { maybeRunRelayPhase(ctx, stderr) },
		dispatch: func() {
			if app.hooksDisp != nil {
				app.hooksDisp.Dispatch(ctx)
			}
		},
	})

	plan := daemonLoopPlan{label: "mtix daemon", interval: intervalSec, pass: pass, controls: control, sched: sched, onContact: onContact}
	if listen {
		l, err := newDaemonListener(hub, intervalSec)
		if err != nil {
			return err
		}
		plan.done = l.Done
	}
	return runDaemonPassLoop(ctx, stdout, stderr, plan)
}

// daemonPassSteps are the four phases of one daemon pass. They are fields so
// a test can count them without a hub.
type daemonPassSteps struct {
	drain, pull, relay, dispatch func()
}

// buildDaemonPass returns the locked pass body. The hub pull runs only when a
// hub is configured AND the scheduler allowed contact on this pass (the
// scale-to-zero guarantee); drain, relay and dispatch are local and run on
// every pass.
func buildDaemonPass(hub bool, st daemonPassSteps) func(contact bool) {
	return func(contact bool) {
		st.drain()
		if hub && contact {
			st.pull()
		}
		st.relay()
		st.dispatch()
	}
}

func daemonPreflight(args []string) (bool, error) {
	if app.mtixDir == "" {
		return false, fmt.Errorf("mtix daemon: not in an mtix project (run 'mtix init' first)")
	}
	if app.store == nil {
		return false, fmt.Errorf("mtix daemon: local store not initialized")
	}
	if _, err := resolveSyncDSN(args); err != nil {
		if !errors.Is(err, transport.ErrDSNNotConfigured) {
			return false, fmt.Errorf("mtix daemon: hub DSN: %w", err)
		}
		return false, nil
	}
	return true, nil
}

// daemonPassControl is optional, instance-scoped cadence/observation input.
// Commands use no override, so production always retains its timer cadence.
type daemonPassControl struct {
	ticks    <-chan time.Time
	observed func(bool)
	listen   bool // mtix daemon --listen: serve waiters, exit with the last lease (MTIX-112.20.4)
}

type daemonLoopPlan struct {
	label    string
	interval int
	// pass runs one locked pass; contact says whether it may dial the hub.
	pass     func(contact bool)
	controls []daemonPassControl
	// sched, when set, replaces the fixed ticker (MTIX-112.20.1); nil keeps
	// the fixed cadence and contacts the hub on every pass.
	sched     *service.ContactScheduler
	onContact func()        // called after a pass that contacted the hub
	localTick time.Duration // local-work tick under sched; default interval seconds
	// done, when set, is checked after every pass and ends the loop (listen mode).
	done func() (bool, string)
}

func (p daemonLoopPlan) localTickOrDefault() time.Duration {
	if p.localTick > 0 {
		return p.localTick
	}
	return time.Duration(p.interval) * time.Second
}

func runDaemonPassLoop(ctx context.Context, stdout, stderr io.Writer, plan daemonLoopPlan) error {
	var control daemonPassControl
	if len(plan.controls) > 0 {
		control = plan.controls[0]
	}
	var timer *time.Timer
	if control.ticks == nil {
		if plan.sched != nil {
			// MTIX-112.20.1: the ContactScheduler, not a fixed ticker, sets the cadence.
			timer = time.NewTimer(time.Hour)
			control.ticks = timer.C
			defer timer.Stop()
		} else {
			ticker := time.NewTicker(time.Duration(plan.interval) * time.Second)
			defer ticker.Stop()
			control.ticks = ticker.C
		}
	}
	run := func() bool {
		runScheduledPass(ctx, stderr, plan, control)
		if listenerDone(stdout, plan) {
			return true
		}
		if timer != nil {
			timer.Reset(plan.sched.Delay(plan.localTickOrDefault()))
		}
		return false
	}
	if run() {
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(stdout, "%s: shutting down\n", plan.label)
			return nil
		case _, open := <-control.ticks:
			if !open {
				return fmt.Errorf("%s: cadence closed", plan.label)
			}
			if run() {
				return nil
			}
		}
	}
}

// listenerDone asks a listen-mode plan whether the loop must end, and says why
// on stdout (MTIX-112.20.4). A plan without a done check never ends this way.
func listenerDone(stdout io.Writer, plan daemonLoopPlan) bool {
	if plan.done == nil {
		return false
	}
	done, why := plan.done()
	if done {
		fmt.Fprintf(stdout, "%s: %s; exiting\n", plan.label, why)
	}
	return done
}

// runScheduledPass runs one locked pass. The scheduler (when present) says
// whether this pass may contact the hub; local work runs either way. A
// contact is recorded only when the pass really ran, so lock contention
// retries at the next tick.
func runScheduledPass(ctx context.Context, stderr io.Writer, plan daemonLoopPlan, control daemonPassControl) {
	contact := true
	if plan.sched != nil {
		contact = plan.sched.Decide().Contact
	}
	acquired := runLockedDaemonPass(ctx, stderr, func() { plan.pass(contact) })
	if acquired && contact && plan.sched != nil {
		plan.sched.MarkContact()
		if plan.onContact != nil {
			plan.onContact()
		}
	}
	if control.observed != nil {
		control.observed(acquired)
	}
}

func runLockedDaemonPass(ctx context.Context, stderr io.Writer, pass func()) bool {
	lock, err := pushlock.AcquireNamed(app.mtixDir, "dispatch.lock")
	if errors.Is(err, pushlock.ErrLockHeld) {
		return false
	}
	if err != nil {
		fmt.Fprintf(stderr, "daemon: dispatch lock error (will retry): %s\n", err)
		return false
	}
	defer func() {
		if err := lock.Release(); err != nil {
			fmt.Fprintf(stderr, "daemon: release dispatch lock: %s\n", err)
		}
	}()
	if ctx.Err() == nil {
		pass()
	}
	return true
}
