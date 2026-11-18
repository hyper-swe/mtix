// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/hyper-swe/mtix/internal/format"
	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/procinfo"
)

// Listener states reported by `mtix daemon status --listener` (MTIX-276).
const (
	listenerStateRunning    = "running"
	listenerStateStopped    = "stopped"
	listenerStateDaemon     = "daemon"
	listenerStateUnverified = "unverified"
)

var (
	// errListenerNotRunning means the daemon lock is free and nothing runs
	// (exit 10).
	errListenerNotRunning = errors.New("no detached listener is running")
	// errListenerUnverified means the daemon lock is held by a process that
	// could not be proven to be this project's listener (exit 8).
	errListenerUnverified = errors.New("the process holding the daemon lock is not a verified listener")
	// errListenerDaemonHeld means the daemon lock is held by a daemon that is
	// not a listener: the OS service or a foreground daemon (exit 12).
	errListenerDaemonHeld = errors.New("the daemon lock is held by a daemon that is not a listener")
	// errListenerRespawned means a live waiter started a new listener right
	// after the stop (exit 9): end the waiters first.
	errListenerRespawned = errors.New("a live waiter started a new listener")
	// errListenerStillRunning means the listener did not end within the time allowed.
	errListenerStillRunning = errors.New("the listener is still running")
)

// listenerCtl lists and stops this project's detached listener. Every
// dependency is injected, so the protocol is tested without processes, signals
// or a clock (MTIX-276).
type listenerCtl struct {
	goos       string
	projectDir string
	lockHeld   func() (bool, error)
	readPID    func() (int, error) // the diagnostic pid file; 0 when absent
	info       func(pid int) (procinfo.Proc, error)
	leases     func() ([]oplocal.ListenLease, error) // live waiter leases
	signal     func(pid int) error                   // SIGTERM; never called on windows
	writeStop  func() error                          // write the stop request file
	clearStop  func() error                          // remove it (absent is fine)
	now        func() time.Time
	sleep      func(time.Duration)
}

// listenerWaiter is one live waiter lease.
type listenerWaiter struct {
	PID      int    `json:"pid"`
	Agent    string `json:"agent,omitempty"`
	LastSeen string `json:"last_seen"`
}

// listenerReport is what status and stop print (and --json encodes).
type listenerReport struct {
	State     string           `json:"state"`
	PID       int              `json:"pid,omitempty"`
	StartTime string           `json:"start_time,omitempty"`
	Detail    string           `json:"detail,omitempty"`
	Waiters   []listenerWaiter `json:"waiters"`
	Next      string           `json:"next,omitempty"`
}

// inspect reads the lock, the pid file, the process identity and the live
// waiter leases. It never contacts the hub and never writes (MTIX-276).
func (c *listenerCtl) inspect() (listenerReport, error) {
	rep := listenerReport{State: listenerStateStopped, Waiters: []listenerWaiter{}}
	leases, err := c.leases()
	if err != nil {
		return rep, fmt.Errorf("list waiter leases: %w", err)
	}
	for _, l := range leases {
		rep.Waiters = append(rep.Waiters, listenerWaiter{PID: l.PID, Agent: l.Agent, LastSeen: l.LastSeen().UTC().Format(time.RFC3339)})
	}
	held, err := c.lockHeld()
	if err != nil {
		return rep, fmt.Errorf("check the daemon lock: %w", err)
	}
	if !held {
		rep.Detail = "no listener is running"
		return rep, nil
	}
	c.identify(&rep)
	return rep, nil
}

// identify fills rep for a held lock: the pid file names the owner, and the
// process's own arguments and start time prove what it is.
func (c *listenerCtl) identify(rep *listenerReport) {
	rep.State = listenerStateUnverified
	pid, err := c.readPID()
	if err != nil || pid <= 0 {
		rep.Detail = "the daemon lock is held but its pid file names no process"
		return
	}
	rep.PID = pid
	proc, err := c.info(pid)
	if err != nil {
		rep.Detail = "the process in the pid file cannot be read (" + format.SafeLine(err.Error()) + ")"
		return
	}
	role, why := procinfo.ClassifyMtixDaemon(proc, c.projectDir)
	rep.StartTime = proc.StartTime
	switch role {
	case procinfo.RoleListener:
		rep.State = listenerStateRunning
	case procinfo.RoleDaemon:
		rep.State = listenerStateDaemon
		rep.Detail = "the daemon lock is held by a daemon, not a listener"
		rep.Next = "mtix daemon stop"
	default:
		rep.Detail = "the lock holder is not verified: " + why
	}
}

// status prints the listener's state and returns a typed error when no
// verified listener runs, so a recovery step can rely on the exit code.
func (c *listenerCtl) status(out io.Writer, asJSON bool) error {
	rep, err := c.inspect()
	if err != nil {
		return err
	}
	return c.finishStatus(out, rep, asJSON)
}

func (c *listenerCtl) finishStatus(out io.Writer, rep listenerReport, asJSON bool) error {
	if rep.State == listenerStateRunning {
		rep.Next = "mtix daemon stop --listener"
	}
	if err := printListenerReport(out, rep, asJSON); err != nil {
		return err
	}
	switch rep.State {
	case listenerStateRunning:
		return nil
	case listenerStateUnverified:
		return errListenerUnverified
	case listenerStateDaemon:
		return errListenerDaemonHeld
	}
	return errListenerNotRunning
}

// printListenerReport writes rep as one JSON object or as plain lines.
func printListenerReport(out io.Writer, rep listenerReport, asJSON bool) error {
	if asJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rep); err != nil {
			return fmt.Errorf("write listener report: %w", err)
		}
		return nil
	}
	switch rep.State {
	case listenerStateRunning:
		fmt.Fprintf(out, "listener running: pid %d, started %s\n", rep.PID, format.SafeLine(rep.StartTime))
	case listenerStateStopped:
		fmt.Fprintf(out, "%s\n", rep.Detail)
	default:
		fmt.Fprintf(out, "no listener is running that can be verified: %s", format.SafeLine(rep.Detail))
		if rep.PID > 0 {
			fmt.Fprintf(out, " (pid %s)", strconv.Itoa(rep.PID))
		}
		fmt.Fprintln(out)
	}
	printWaiters(out, rep.Waiters)
	if rep.Next != "" {
		fmt.Fprintf(out, "next: %s\n", rep.Next)
	}
	return nil
}

func printWaiters(out io.Writer, waiters []listenerWaiter) {
	if len(waiters) == 0 {
		fmt.Fprintln(out, "waiters: none live")
		return
	}
	for _, w := range waiters {
		fmt.Fprintf(out, "waiter: pid %d agent %s last seen %s\n", w.PID, format.SafeLine(w.Agent), w.LastSeen)
	}
}
