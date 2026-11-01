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
const quarantineListPage = sqlite.QuarantineAcknowledgementPageSize

// QuarantinedEvent is one event held in the local quarantine
// (sync_quarantine) as `mtix sync quarantine list` shows it: a pulled event
// (MTIX-95.11) or an own event push holds (source push, MTIX-95.12). It
// carries the event's id, node, op and Lamport clock (read from the raw
// event), the pass that first quarantined it, why, how many attempts
// failed, when it was first seen and last attempted, and the version of
// mtix that first quarantined it.
type QuarantinedEvent struct {
	EventID         string                     `json:"event_id"`
	Handle          string                     `json:"handle"`
	EventIDSHA256   string                     `json:"event_id_sha256"`
	NodeID          string                     `json:"node_id"`
	OpType          string                     `json:"op_type"`
	LamportClock    int64                      `json:"lamport_clock"`
	Source          string                     `json:"source"`
	Reason          string                     `json:"reason"`
	Attempts        int                        `json:"attempts"`
	FirstSeen       string                     `json:"first_seen"`
	LastAttempt     string                     `json:"last_attempt"`
	CLIVersion      string                     `json:"cli_version"`
	Acknowledgement *QuarantineAcknowledgement `json:"acknowledgement,omitempty"`
}

// ListQuarantined returns every quarantined event, held push events
// included, in Lamport clock order, then event id, for `mtix sync
// quarantine list` (MTIX-95.11, MTIX-95.12). It only reads: it never retries, removes or rewrites a
// row, and it contacts no hub. An empty quarantine is an empty, non-nil
// slice.
func (s *SyncService) ListQuarantined(ctx context.Context) ([]QuarantinedEvent, error) {
	return s.listQuarantined(ctx, QuarantineHandleLen)
}

// listQuarantined is ListQuarantined with a settable handle base length, so a
// test can force handle collisions that a 12-character base makes impractical.
func (s *SyncService) listQuarantined(ctx context.Context, handleBase int) ([]QuarantinedEvent, error) {
	out := []QuarantinedEvent{}
	var after *sqlite.QuarantineKey
	for {
		page, err := s.store.QuarantinePage(ctx, after, quarantineListPage)
		if err != nil {
			return nil, fmt.Errorf("list quarantined events: %w", err)
		}
		if len(page) == 0 {
			ids := make([]string, len(out))
			for i := range out {
				ids[i] = out[i].EventID
			}
			for i, h := range quarantineHandles(ids, handleBase) {
				out[i].Handle = h
			}
			return out, nil
		}
		acks, err := s.store.QuarantineAcknowledgementsFor(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("list quarantine acknowledgements: %w", err)
		}
		for _, q := range page {
			var receipt *QuarantineAcknowledgement
			if ack, ok := acks[q.EventID]; ok {
				converted := quarantineAcknowledgementOutput(ack)
				receipt = &converted
			}
			out = append(out, QuarantinedEvent{
				EventID: q.EventID, EventIDSHA256: QuarantineHandleFull(q.EventID),
				NodeID: q.NodeID, OpType: q.OpType, LamportClock: q.Lamport,
				Source: q.Source, Reason: q.Reason, Attempts: q.Attempts,
				FirstSeen: q.FirstSeen, LastAttempt: q.LastAttempt, CLIVersion: q.CLIVersion,
				Acknowledgement: receipt,
			})
		}
		last := page[len(page)-1]
		after = &sqlite.QuarantineKey{Lamport: last.Lamport, EventID: last.EventID}
	}
}
