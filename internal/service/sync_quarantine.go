// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// quarantineListPage is how many quarantine rows ListQuarantined reads per
// query.
const quarantineListPage = 500

// QuarantinedEvent is one pulled event held in the local quarantine
// (sync_quarantine, MTIX-95.11) as `mtix sync quarantine list` shows it:
// the event's id, node, op and Lamport clock (read from the raw event as
// pulled), the pass that first quarantined it, why, how many attempts
// failed, when it was first seen and last attempted, and the version of
// mtix that first quarantined it.
type QuarantinedEvent struct {
	EventID      string `json:"event_id"`
	NodeID       string `json:"node_id"`
	OpType       string `json:"op_type"`
	LamportClock int64  `json:"lamport_clock"`
	Source       string `json:"source"`
	Reason       string `json:"reason"`
	Attempts     int    `json:"attempts"`
	FirstSeen    string `json:"first_seen"`
	LastAttempt  string `json:"last_attempt"`
	CLIVersion   string `json:"cli_version"`
}

// ListQuarantined returns every quarantined pulled event in retry order
// (Lamport clock, then event id), for `mtix sync quarantine list`
// (MTIX-95.11). It only reads: it never retries, removes or rewrites a
// row, and it contacts no hub. An empty quarantine is an empty, non-nil
// slice.
func (s *SyncService) ListQuarantined(ctx context.Context) ([]QuarantinedEvent, error) {
	out := []QuarantinedEvent{}
	var after *sqlite.QuarantineKey
	for {
		page, err := s.store.QuarantinePage(ctx, after, quarantineListPage)
		if err != nil {
			return nil, fmt.Errorf("list quarantined events: %w", err)
		}
		if len(page) == 0 {
			return out, nil
		}
		for _, q := range page {
			out = append(out, QuarantinedEvent{
				EventID: q.EventID, NodeID: q.NodeID, OpType: q.OpType, LamportClock: q.Lamport,
				Source: q.Source, Reason: q.Reason, Attempts: q.Attempts,
				FirstSeen: q.FirstSeen, LastAttempt: q.LastAttempt, CLIVersion: q.CLIVersion,
			})
		}
		last := page[len(page)-1]
		after = &sqlite.QuarantineKey{Lamport: last.Lamport, EventID: last.EventID}
	}
}
