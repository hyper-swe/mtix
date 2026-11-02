// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// addressedPushTimeout bounds one in-process push of an addressed send.
const addressedPushTimeout = 5 * time.Minute

// Defaults for waiting out a push that holds the push lock: the child retries
// a lock probe locally (no hub contact) for about this long before it records
// that the message is still pending.
const (
	heldPushTries = 20
	heldPushWait  = time.Second
)

// errPushHeld marks a send whose push never ran because another push held the
// lock for the whole wait: the message is pending, not pushed.
var errPushHeld = errors.New("push lock held")

// errPushIncomplete marks a push that ran but did not drain the queue (a batch
// that made no headway, or events waiting on a held creation): the message may
// still be pending, so it is not a success.
var errPushIncomplete = errors.New("push did not drain the queue")

// pushProbeKey carries a pushProbe through a push, so an addressed send can
// tell held from pushed and drained from undrained.
type pushProbeKey struct{}

// pushProbe is what a push reports to the addressed send that started it.
type pushProbe struct {
	held      atomic.Bool // pushWithHandoff skipped its pass: the push lock was held
	undrained atomic.Bool // the last pass that ran ended without draining the queue
}

// addressedRunOptions are the knobs of one addressed-send push run.
type addressedRunOptions struct {
	Transport transport.Options
	Now       func() time.Time
	HeldTries int
	HeldWait  time.Duration
	Wait      func(time.Duration) // injected pause between lock probes; nil means time.Sleep
	// Push runs one push; nil means runSyncPush. Tests inject it.
	Push func(ctx context.Context, stderr io.Writer, opts transport.Options) error
}

func (o addressedRunOptions) push(ctx context.Context, stderr io.Writer) error {
	if o.Push != nil {
		return o.Push(ctx, stderr, o.Transport)
	}
	return runSyncPush(ctx, io.Discard, stderr, nil, o.Transport, false)
}

func (o addressedRunOptions) pause() {
	if o.Wait != nil {
		o.Wait(o.HeldWait)
		return
	}
	time.Sleep(o.HeldWait)
}

// runAddressedSendPush is the body of `mtix sync push --on-addressed-send`
// and of the in-process executors: push the board's whole pending prefix (the
// same Lamport-ordered pass as mtix sync push, so a comment never overtakes
// its node's create), run one relay pass on a relay board, then persist the
// outcome operator-locally. Output goes to stderr only; the outcome is what a
// later command and doctor show (ADR-007 D21, MTIX-112.20.2).
func runAddressedSendPush(ctx context.Context, stderr io.Writer, o addressedRunOptions) error {
	var pushErr error
	if hubConfigured(app.mtixDir) {
		pushErr = pushWaitingForLock(ctx, stderr, o)
		if pushErr == nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			pushErr = ctx.Err() // the deadline cut the run short: record a timeout, not a success
		}
	}
	// An addressed send publishes even when sync.relay.poll_interval is 0:
	// that setting only stops the daemon polling. A relay failure is part of
	// the outcome, so neither half can hide the other's failure.
	relayErr := runRelayPhase(ctx, stderr)
	outcome := errors.Join(pushErr, relayErr)
	if errors.Is(ctx.Err(), context.Canceled) {
		return outcome // shutting down: an interrupted run records nothing
	}
	if err := recordAddressedPushOutcome(app.mtixDir, o.Now, pushErr, relayErr); err != nil {
		fmt.Fprintf(stderr, "mtix: could not record the addressed push outcome: %v\n", err)
	}
	return outcome
}

// pushWaitingForLock runs the push, and when another push holds the lock it
// probes again for a bounded time: the holder drains arrivals only within its
// own pass budget, so a send that lands at its cap must push itself once the
// lock is free. A lock held for the whole wait is reported as errPushHeld,
// never as success.
func pushWaitingForLock(ctx context.Context, stderr io.Writer, o addressedRunOptions) error {
	probe := &pushProbe{}
	ctx = context.WithValue(ctx, pushProbeKey{}, probe)
	for try := 0; ; try++ {
		// Nothing pending (an earlier push already sent it): no lock, no hub contact.
		pending, readErr := readPendingBatch(ctx, app.store, 1)
		if readErr != nil {
			return fmt.Errorf("read pending events: %w", readErr)
		}
		if len(pending) == 0 {
			return nil
		}
		probe.held.Store(false)
		probe.undrained.Store(false)
		if err := o.push(ctx, stderr); err != nil {
			return err
		}
		if probe.held.Load() {
			if try >= o.HeldTries || ctx.Err() != nil {
				return errPushHeld
			}
			o.pause()
			continue
		}
		if probe.undrained.Load() {
			return errPushIncomplete
		}
		return nil
	}
}

// coalescingRunner runs a job on a background goroutine so that a burst of
// triggers never runs two jobs at once and never loses one: a trigger that
// arrives while the job runs marks it dirty and the runner goes around once
// more after the job returns (ADR-007 D21, scale to zero: one hub contact per
// burst, not per send).
type coalescingRunner struct {
	mu      sync.Mutex
	running bool
	dirty   bool
	job     func()
	done    func() // optional, called when the runner goes idle (tests)
}

// Trigger requests one more run of the job and returns at once.
func (r *coalescingRunner) Trigger() {
	r.mu.Lock()
	if r.running {
		r.dirty = true
		r.mu.Unlock()
		return
	}
	r.running = true
	r.mu.Unlock()
	go r.loop()
}

func (r *coalescingRunner) loop() {
	for {
		r.job()
		r.mu.Lock()
		if !r.dirty {
			r.running = false
			r.mu.Unlock()
			if r.done != nil {
				r.done()
			}
			return
		}
		r.dirty = false
		r.mu.Unlock()
	}
}

// inProcessAddressedSendHook is the executor of a long-lived process (the MCP
// server, mtix serve): it pushes in-process (ADR-007 D21 executor order, first
// step) and returns before the network work finishes. It returns the state
// word for the caller to show.
func inProcessAddressedSendHook() func(context.Context, string) string {
	runner := &coalescingRunner{job: func() {
		ctx, cancel := context.WithTimeout(context.Background(), addressedPushTimeout)
		defer cancel()
		_ = runAddressedSendPush(ctx, io.Discard, defaultAddressedRun()) // the outcome is persisted inside
	}}
	return func(ctx context.Context, _ string) string {
		res := requestAddressedPush(addressedPushDeps{
			inputs:    func() (addressedPushInputs, error) { return productionAddressedPushInputs(ctx) },
			inProcess: runner.Trigger,
		})
		if res.Reason != "" {
			return res.State + " (" + res.Reason + ")"
		}
		return res.State
	}
}

// mcpAddressedSendHook adapts the in-process executor to the MCP tool.
func mcpAddressedSendHook() mcp.AddressedSendHook {
	return mcp.AddressedSendHook(inProcessAddressedSendHook())
}

// defaultAddressedRun is the production run configuration.
func defaultAddressedRun() addressedRunOptions {
	return addressedRunOptions{Now: utcNow, HeldTries: heldPushTries, HeldWait: heldPushWait}
}
