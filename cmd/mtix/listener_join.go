// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
)

// listenerRecheckEvery is how often a parked waiter re-probes for a listener.
// The probe is a local lock check, never a hub contact.
const listenerRecheckEvery = 10 * time.Second

// listenerPlan says whether a waiter joins a per-board listener, and why not.
type listenerPlan struct {
	Join   bool
	Reason string // set when Join is false and the opt-out or state is worth telling
	Quiet  bool   // nothing to listen to: no line is printed
}

// planListener decides from the board's config and what it syncs through
// whether a waiter should arm a lease and start a listener (MTIX-112.20.4).
// It reads only local config; it never contacts the hub.
func planListener(cfg *service.ConfigService, hub, relay bool, daemonInterval time.Duration) listenerPlan {
	if !hub && !relay {
		return listenerPlan{Quiet: true, Reason: "no hub or relay is configured"}
	}
	mode, _, err := cfg.HubContact(daemonInterval)
	if err != nil {
		return listenerPlan{Reason: "sync.hub_contact is invalid; fix: mtix doctor"}
	}
	if mode.Kind == service.HubContactManual {
		return listenerPlan{Reason: "sync.hub_contact is manual, so no listener starts"}
	}
	listen, err := cfg.ListenSettings()
	if err != nil {
		return listenerPlan{Reason: "sync.listen settings are invalid; fix: mtix doctor"}
	}
	if !listen.Hold {
		return listenerPlan{Reason: "sync.listen is off, so no listener starts"}
	}
	return listenerPlan{Join: true}
}

// listenerJoin is the waiter side of the per-board listener: it arms one
// lease for this waiter process and starts `mtix daemon --listen` when none
// runs. Every dependency is injected so the protocol is testable without
// processes, clocks or a hub.
type listenerJoin struct {
	state     *oplocal.State
	key       string
	agent     string
	pid       int
	now       func() time.Time
	startTime func(pid int) (string, error)
	plan      func() listenerPlan
	running   func() (bool, error) // is a daemon or listener holding the lifetime lock
	// idleClamped reports that a listener started now would exit at once (the
	// hub idle clamp has passed, or nothing was ever recorded); nil means never.
	idleClamped func() bool
	spawn       func() error
	out         io.Writer
}

// Join arms the lease, starts the listener when absent, and returns the
// function that ends the lease. It never fails the wait: every problem is
// reported on out in one line and the wait carries on with the local inbox.
// refresh delivers keep-alive ticks (nil disables them); a crashed waiter
// stops refreshing and its lease expires after oplocal.ListenLeaseTTL.
// recheck delivers liveness checks (nil disables them): a waiter that parked
// behind a listener that was just deciding to exit would otherwise be left
// with no listener, so each tick re-probes the lifetime lock and starts a
// listener when the holder is gone.
func (j *listenerJoin) Join(refresh, recheck <-chan time.Time) (leave func()) {
	p := j.plan()
	if !p.Join {
		if !p.Quiet {
			fmt.Fprintf(j.out, "mtix: listener not started: %s\n", p.Reason)
		}
		return func() {}
	}
	st, err := j.startTime(j.pid)
	if err != nil {
		fmt.Fprintf(j.out, "mtix: listener not started: cannot identify this waiter (%v)\n", err)
		return func() {}
	}
	lease := oplocal.ListenLease{PID: j.pid, StartTime: st, Agent: j.agent, ArmedAt: j.now()}
	if err := j.state.WriteListenLease(j.key, lease); err != nil {
		fmt.Fprintf(j.out, "mtix: listener not started: cannot write the waiter lease (%v); fix: mtix doctor\n", err)
		return func() {}
	}
	if j.clamped() {
		fmt.Fprintf(j.out, "mtix: hub listener not started: the hub is idle-clamped (no recent activity); it starts on a later wait or recheck once real activity arrives\n")
	} else {
		j.report(j.ensureListener())
	}
	stop := make(chan struct{})
	finished := make(chan struct{})
	go j.keepAlive(lease, refresh, recheck, stop, finished)
	return func() {
		close(stop)
		<-finished
		if err := j.state.RemoveListenLease(j.key, j.pid); err != nil {
			fmt.Fprintf(j.out, "mtix: could not remove the waiter lease (%v); it expires on its own\n", err)
		}
	}
}

// ensureListener returns "" when a listener already runs, "started" after a
// spawn, or an error text.
func (j *listenerJoin) ensureListener() (string, error) {
	held, err := j.running()
	if err != nil {
		return "", fmt.Errorf("check for a running listener: %w", err)
	}
	if held {
		return "already running", nil
	}
	if err := j.spawn(); err != nil {
		return "", fmt.Errorf("start the listener: %w", err)
	}
	return "started", nil
}

func (j *listenerJoin) report(state string, err error) {
	if err != nil {
		fmt.Fprintf(j.out, "mtix: listener not started: %v; fix: mtix daemon --listen\n", err)
		return
	}
	fmt.Fprintf(j.out, "mtix: hub listener %s for this board; it exits when the last waiter ends (opt out: mtix config set sync.listen off)\n", state)
}

func (j *listenerJoin) keepAlive(lease oplocal.ListenLease, refresh, recheck <-chan time.Time, stop <-chan struct{}, finished chan<- struct{}) {
	defer close(finished)
	failed := false
	for {
		select {
		case <-stop:
			return
		case <-recheck:
			failed = j.recheck(failed)
		case <-refresh:
			lease.RefreshedAt = j.now()
			if err := j.state.WriteListenLease(j.key, lease); err != nil {
				fmt.Fprintf(j.out, "mtix: could not refresh the waiter lease (%v)\n", err)
			}
		}
	}
}

func (j *listenerJoin) clamped() bool { return j.idleClamped != nil && j.idleClamped() }

// recheck re-probes for a running listener and starts one when the holder has
// gone. A start is reported once; a failure is reported once until it clears,
// so a broken spawn cannot flood the waiter's stderr.
func (j *listenerJoin) recheck(failedBefore bool) bool {
	if j.clamped() {
		return failedBefore // quiet park: a listener would exit at once, so none is respawned
	}
	state, err := j.ensureListener()
	switch {
	case err != nil && !failedBefore:
		j.report("", err)
	case err == nil && state == "started":
		fmt.Fprintf(j.out, "mtix: hub listener restarted for this board after the previous one exited\n")
	}
	return err != nil
}

// productionListenerJoin wires the join to this process's board. It returns
// nil when there is no board or no operator state to arm a lease in.
func productionListenerJoin(agent string, out io.Writer) (*listenerJoin, error) {
	if app.mtixDir == "" || app.configSvc == nil {
		return nil, nil
	}
	src, err := newHubContactSource(app.mtixDir)
	if err != nil {
		return nil, err
	}
	hub, relay := hubConfigured(app.mtixDir), relayDir() != ""
	idle := func() bool {
		if relay {
			return false // a relay board's listener stays up while a waiter lives
		}
		ended, _ := holdEnded(src)
		return ended
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("locate the mtix binary: %w", err)
	}
	return &listenerJoin{
		state: src.state, key: src.key(), agent: agent, pid: os.Getpid(),
		now: src.now, startTime: procinfo.StartTime, out: out,
		plan:    func() listenerPlan { return planListener(app.configSvc, hub, relay, src.grandfatherInterval) },
		running: func() (bool, error) { return daemonLockHeld(src.mtixDir) }, idleClamped: idle,
		spawn: func() error { return spawnListener(exe, filepath.Dir(src.mtixDir)) },
	}, nil
}

// joinListener is called by a waiter before it blocks (MTIX-112.20.4). All
// problems are one-line notes on out; the returned function ends the lease.
func joinListener(agent string, out io.Writer) func() {
	j, err := productionListenerJoin(agent, out)
	if err != nil || j == nil {
		if err != nil {
			fmt.Fprintf(out, "mtix: listener not started: %v\n", err)
		}
		return func() {}
	}
	refresh := time.NewTicker(oplocal.ListenLeaseRefreshEvery)
	recheck := time.NewTicker(listenerRecheckEvery)
	end := j.Join(refresh.C, recheck.C)
	return func() { end(); refresh.Stop(); recheck.Stop() }
}
