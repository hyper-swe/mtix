// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
)

// runRelayPhase runs one relay pass inside a daemon tick, if a relay is
// configured (FR-21 §6.6).
//
// It sits between the hub pull and hook dispatch, so the order over a
// tick is publish → ingest → dispatch: this peer's work reaches the
// medium, then everyone else's arrives as journal rows, then the ledger
// fires hooks over everything new — including what just came in over a
// folder. That last step needs no knowledge of the relay at all, which
// is the point.
//
// Nothing here can fail a tick. A relay problem is transport-degraded
// by construction (§6.2): it is reported, counted, and retried next
// tick, and it must never stop the hub pull or hook dispatch that share
// the pass.
func runRelayPhase(ctx context.Context, stderr io.Writer) {
	if relayDir() == "" {
		return
	}
	stack, err := openRelayStack()
	if err != nil {
		fmt.Fprintf(stderr, "mtix daemon: relay unavailable: %s\n", err)
		return
	}
	res, err := stack.Pass(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "mtix daemon: relay pass: %s\n", err)
	}
	if app.logger != nil && (res.Published > 0 || res.Ingest.Applied > 0) {
		app.logger.Info("relay pass",
			slog.Int("published", res.Published),
			slog.Int("applied", res.Ingest.Applied),
			slog.Int("quarantined", res.Ingest.Quarantined),
			slog.Int("auth_failures", res.Ingest.AuthFailures),
		)
	}
	for _, s := range res.Ingest.Stalls {
		fmt.Fprintf(stderr, "mtix daemon: relay stalled on %s (%s): %s\n  fix: %s\n",
			s.PeerID, s.Code, s.Reason, relayStallRecovery(s.Code))
	}
}

// relayPollDisabled reports whether this peer has declared itself
// tick-only (D-R12). Such a peer converges on its next manual pass; the
// daemon simply has no relay work to do, which is a deployment rather
// than a fault.
func relayPollDisabled() bool {
	return relayConfigInt("sync.relay.poll_interval", 5) == 0
}

// maybeRunRelayPhase runs the relay phase unless this peer is tick-only.
func maybeRunRelayPhase(ctx context.Context, stderr io.Writer) {
	if relayPollDisabled() {
		return
	}
	runRelayPhase(ctx, stderr)
}
