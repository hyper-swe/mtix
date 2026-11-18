// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"
	"io"
	"time"
)

const (
	// listenerStopTimeout is how long stop waits for the cooperative exit
	// (one pass is about 5 seconds at the default interval).
	listenerStopTimeout = 30 * time.Second
	// listenerTermWait is how long stop waits after SIGTERM.
	listenerTermWait = 5 * time.Second
	// listenerRespawnWatch covers one waiter recheck (listenerRecheckEvery)
	// with margin: a live waiter restarts a missing listener inside it.
	listenerRespawnWatch = listenerRecheckEvery + 2*time.Second
	listenerPollEvery    = 200 * time.Millisecond
)

// listenerStopOpts carries the flags of `mtix daemon stop --listener`.
type listenerStopOpts struct {
	timeout time.Duration
}

// stop ends this project's detached listener and verifies the daemon lock is
// free (MTIX-276). It asks the listener to exit through a request file that
// only listen mode reads, so no signal is needed on any OS. Only when that
// times out, and only for a listener whose pid, arguments, working directory
// and start time still match what status showed, does it send SIGTERM (never
// on windows). Anything unverified fails closed with no signal.
func (c *listenerCtl) stop(out io.Writer, o listenerStopOpts, asJSON bool) error {
	rep, err := c.inspect()
	if err != nil {
		return err
	}
	switch rep.State {
	case listenerStateStopped:
		return printListenerReport(out, rep, asJSON)
	case listenerStateDaemon:
		if perr := printListenerReport(out, rep, asJSON); perr != nil {
			return perr
		}
		return errListenerDaemonHeld
	}
	if err := c.writeStop(); err != nil {
		return fmt.Errorf("write the stop request: %w", err)
	}
	if !c.waitFree(o.timeout) {
		if err := c.escalate(out, rep); err != nil {
			return errors.Join(err, c.clearStop())
		}
	}
	if err := c.clearStop(); err != nil {
		return fmt.Errorf("remove the stop request: %w", err)
	}
	return c.afterStop(out, asJSON)
}

// waitFree polls the daemon lock until it is free or d has passed. A probe
// error is never read as free: only a successful probe that finds the lock
// free ends the wait with success.
func (c *listenerCtl) waitFree(d time.Duration) bool {
	deadline := c.now().Add(d)
	for {
		held, err := c.lockHeld()
		if err == nil && !held {
			return true
		}
		if !c.now().Before(deadline) {
			return false
		}
		c.sleep(listenerPollEvery)
	}
}

// escalate sends SIGTERM to the listener status identified, after proving it
// is still the same verified process, then waits for the lock to be free.
// Residual window: the identity is re-read just before the signal, and a
// process cannot be signaled atomically with that read, so the listener could
// exit and its pid be reused between the two. An unrelated process that took
// the pid in that window would receive one SIGTERM. The window is
// microseconds, and a pidfd is not required (it exists on linux only).
func (c *listenerCtl) escalate(out io.Writer, before listenerReport) error {
	fail := func(why string) error {
		fmt.Fprintf(out, "listener still running: %s; nothing was signaled\n", why)
		return errListenerStillRunning
	}
	if c.goos == "windows" {
		return fail(fmt.Sprintf("pid %d did not exit; confirm it is `mtix daemon --listen` and end it from Task Manager", before.PID))
	}
	if before.State != listenerStateRunning {
		return fail("the lock holder was never verified as this project's listener")
	}
	now, err := c.inspect()
	if err != nil {
		return fail("the process could not be re-checked")
	}
	if now.State != listenerStateRunning || now.PID != before.PID || now.StartTime != before.StartTime {
		return fail("the process changed while waiting")
	}
	fmt.Fprintf(out, "listener did not exit on request; sending SIGTERM to the verified listener (pid %d)\n", before.PID)
	// A failed signal (the listener exited first, or permission denied) is not
	// read as an error on its own: the lock decides. Free means the listener
	// ended; held means it is still running (exit 11), never success.
	if err := c.signal(before.PID); err != nil {
		fmt.Fprintf(out, "SIGTERM could not be delivered (%v); re-checking the lock\n", err)
	}
	if !c.waitFree(listenerTermWait) {
		fmt.Fprintf(out, "listener still running after SIGTERM (pid %d)\n", before.PID)
		return errListenerStillRunning
	}
	return nil
}

// afterStop reports the stop. A live waiter restarts a missing listener within
// one recheck, so when one is live the lock is watched for that long; a
// restart is a failure that names the waiters.
func (c *listenerCtl) afterStop(out io.Writer, asJSON bool) error {
	rep, err := c.inspect()
	if err != nil {
		return err
	}
	if len(rep.Waiters) > 0 && c.respawned(listenerRespawnWatch) {
		printWaiters(out, rep.Waiters)
		fmt.Fprintln(out, "a live waiter started a new listener; end those waiters first, then run: mtix daemon stop --listener")
		return errListenerRespawned
	}
	rep.State, rep.PID, rep.StartTime = listenerStateStopped, 0, ""
	rep.Detail = "listener stopped"
	rep.Next = "mtix daemon status --listener (exits 10 when no listener runs)"
	if err := printListenerReport(out, rep, asJSON); err != nil {
		return err
	}
	if len(rep.Waiters) > 0 && !asJSON {
		fmt.Fprintln(out, "live waiters remain; a waiter restarts a listener on its next check, so end them before work that needs no listener")
	}
	return nil
}

// respawned watches the lock for d and reports whether it was taken again.
func (c *listenerCtl) respawned(d time.Duration) bool {
	deadline := c.now().Add(d)
	for c.now().Before(deadline) {
		c.sleep(listenerPollEvery)
		if held, err := c.lockHeld(); err == nil && held {
			return true
		}
	}
	return false
}
