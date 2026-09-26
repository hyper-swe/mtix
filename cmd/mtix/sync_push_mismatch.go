// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// holdMismatches holds each event of events that the hub reported in
// mismatches: its event id is already on the hub, but with another node,
// op or payload (MTIX-95.3; ADR-006 I6; orchestrator decision Q4). Such an
// event is never acknowledged, so it would stay pending and be sent again
// by every push; it is held in sync_quarantine with source push and a
// permanent "refused: " reason that names what differs and the hub copy's
// task and op, and named on stderr (recordBatch). mtix sync doctor fails
// its held push events check while it is held. A held creation holds the
// later changes of its task and subtree too (the subtree rule). It returns
// how many events it newly held.
func holdMismatches(ctx context.Context, stderr io.Writer, store *sqlite.Store,
	events []*model.SyncEvent, mismatches []transport.PresenceMismatch,
) (int, error) {
	if len(mismatches) == 0 {
		return 0, nil
	}
	byID := make(map[string]*model.SyncEvent, len(events))
	for _, e := range events {
		byID[e.EventID] = e
	}
	var d batchHolds
	for _, m := range mismatches {
		e, ok := byID[m.EventID]
		if !ok {
			return 0, fmt.Errorf("the hub reported event %s, which this batch did not send", oneLine(m.EventID))
		}
		q, err := pushHold(e, mismatchReason(m))
		if err != nil {
			return 0, err
		}
		d.holds = append(d.holds, q)
	}
	_, held, err := recordBatch(ctx, stderr, store, d)
	return held, err
}

// mismatchReason is the permanent hold reason of an event whose id the hub
// holds with other content (MTIX-95.3), for example "refused: the hub
// already holds this event id with a different payload (hub copy: PROJ-3
// update_field)". One line, at most maxQuarantineReason runes.
func mismatchReason(m transport.PresenceMismatch) string {
	return quarantineReason(fmt.Errorf("%sthe hub already holds this event id with a different %s (hub copy: %s %s)",
		holdRefusedPrefix, strings.Join(m.Fields, " and "), m.HubNodeID, m.HubOpType))
}
