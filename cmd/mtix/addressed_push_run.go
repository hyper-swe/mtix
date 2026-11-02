// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/hyper-swe/mtix/internal/mcp"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// addressedPushTimeout bounds one in-process push of an addressed send.
const addressedPushTimeout = 5 * time.Minute

// runAddressedSendPush is the body of `mtix sync push --on-addressed-send`
// and of the MCP executor: push the board's whole pending prefix (the same
// Lamport-ordered pass as mtix sync push, so a comment never overtakes its
// node's create), run one relay pass on a relay board, then persist the
// outcome operator-locally. Output goes to stderr only; the outcome is what a
// later command and doctor show (ADR-007 D21, MTIX-112.20.2).
func runAddressedSendPush(ctx context.Context, stderr io.Writer, opts transport.Options, now func() time.Time) error {
	var pushErr error
	if hubConfigured(app.mtixDir) {
		pushErr = runSyncPush(ctx, io.Discard, stderr, nil, opts, false)
	}
	if relayDir() != "" {
		runRelayPhase(ctx, stderr)
	}
	if err := recordAddressedPushOutcome(app.mtixDir, now, pushErr); err != nil {
		fmt.Fprintf(stderr, "mtix: could not record the addressed push outcome: %v\n", err)
	}
	return pushErr
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

// mcpAddressedSendHook is the executor of an addressed mtix_annotate: the MCP
// server is long-lived, so it pushes in-process (ADR-007 D21 executor order,
// first step) and returns before the network work finishes.
func mcpAddressedSendHook() mcp.AddressedSendHook {
	runner := &coalescingRunner{job: func() {
		ctx, cancel := context.WithTimeout(context.Background(), addressedPushTimeout)
		defer cancel()
		_ = runAddressedSendPush(ctx, io.Discard, transport.Options{}, utcNow) // the outcome is persisted inside
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
