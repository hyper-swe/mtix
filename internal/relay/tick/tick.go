// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package tick composes one relay pass: publish, then ingest.
//
// The order is FR-21 §6.6's, and it is not arbitrary. Publishing first
// means a peer's own work reaches the medium before it spends the pass
// reading others', so a tick-only peer that runs exactly one pass per
// session still gets its work out. Ingest then leaves fresh journal
// rows behind, which is what the dispatch phase after it fires hooks
// from — so a single pass can carry an event from another machine all
// the way to a local wake.
//
// Dispatch itself is deliberately NOT here. Relay-arrived events are
// ordinary journal rows, and the shipped ledger dispatcher fires their
// hooks with no knowledge that a relay exists; folding it into this
// package would create the coupling the design exists to avoid.
//
// The two halves are independent. A publish failure does not skip
// ingest: the medium being unwritable says nothing about whether it is
// readable, and a peer that cannot publish should still converge on
// what others sent.
package tick

import (
	"context"
	"errors"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
)

// Publisher is the publish half of a pass.
type Publisher interface {
	PublishPending(ctx context.Context) (int, error)
}

// Ingestor is the ingest half of a pass.
type Ingestor interface {
	IngestAll(ctx context.Context) (ingest.Stats, error)
}

// Result is what one pass did.
type Result struct {
	// Published is the number of local events framed onto the medium.
	Published int

	// Ingest is what the ingest half saw, including its quarantine and
	// stall detail.
	Ingest ingest.Stats
}

// Run executes one pass: publish, then ingest.
//
// Both halves always run. Their errors are joined rather than
// short-circuited, so a caller learns about a medium that is unwritable
// AND a peer that is stalled in the same pass instead of discovering
// them one tick at a time. A returned error is a report, not a reason
// to stop ticking: FR-21 §6.2 makes every relay failure retryable by
// construction.
//
// A nil half is skipped, so a peer configured to read but not publish —
// or the reverse — needs no separate code path.
func Run(ctx context.Context, p Publisher, in Ingestor) (Result, error) {
	var res Result
	var errs []error

	if p != nil {
		published, err := p.PublishPending(ctx)
		res.Published = published
		if err != nil {
			errs = append(errs, err)
		}
	}
	if in != nil {
		stats, err := in.IngestAll(ctx)
		res.Ingest = stats
		if err != nil {
			errs = append(errs, err)
		}
	}
	return res, errors.Join(errs...)
}
