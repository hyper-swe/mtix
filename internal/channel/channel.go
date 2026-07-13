// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package channel implements FR-20 §9 rung 4: pushing an agent's inbox events
// into an already-running agent session, so a live agent learns about
// addressed work without polling ("delivery terminates in the prompt").
//
// The package is a pluggable pipeline: a Source yields an agent's undelivered
// inbox events (at-least-once, ordered), and an Adapter pushes each event into
// one harness's session mechanism. The first adapter targets Claude Code
// channels (claudecode.go); when other harnesses grow a push mechanism they
// become sibling adapters over the same Source.
//
// Push is a LATENCY optimization, never a correctness dependency: the inbox
// remains the durable source of truth and acknowledgement stays explicit, so
// a lost push is just a slower wake via the exec cold-start rung.
package channel

import (
	"context"
	"log/slog"
	"time"
)

// Event is one addressed inbox event to push into a session.
type Event struct {
	Seq  int64
	Node string
	From string
	Body string
}

// Source yields batches of an agent's inbox events. Next blocks up to wait for
// new events and returns only events not yet returned by THIS source instance
// (a fresh instance starts over from the unacked backlog — deliberate: a new
// session should be told about outstanding work exactly once).
type Source interface {
	Next(ctx context.Context, wait time.Duration) ([]Event, error)
}

// Adapter pushes one event into a running agent session. Push is fire-and-
// forget from the fabric's point of view: an error is logged by the pump and
// the event is NOT retried here (the durable inbox already guarantees the
// event is never lost; the agent acks only what it actually handled).
type Adapter interface {
	Name() string
	Push(e Event) error
}

// pumpPollInterval bounds one Source.Next block, so ctx cancellation is
// honored promptly even against a source with no native wakeup.
const pumpPollInterval = 5 * time.Second

// Pump drains src into ad until ctx is cancelled. It never returns an error:
// source failures are logged and retried next round; adapter failures are
// logged and skipped (see Adapter).
func Pump(ctx context.Context, src Source, ad Adapter, logger *slog.Logger) {
	if logger == nil {
		logger = slog.Default()
	}
	for {
		if ctx.Err() != nil {
			return
		}
		events, err := src.Next(ctx, pumpPollInterval)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("channel: source read failed; retrying", "adapter", ad.Name(), "error", err)
			continue
		}
		for _, e := range events {
			if err := ad.Push(e); err != nil {
				logger.Warn("channel: push failed (event stays in the inbox)",
					"adapter", ad.Name(), "seq", e.Seq, "error", err)
				continue
			}
			logger.Info("channel: pushed inbox event into session",
				"adapter", ad.Name(), "seq", e.Seq, "node", e.Node, "from", e.From)
		}
	}
}
