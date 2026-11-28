// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/tick"
)

// relayMediumRecovery is the recovery for RELAY_MEDIUM_UNREADABLE (FR-21 §9).
// Nothing on the relay is modified; the stalled peer resumes by itself.
const relayMediumRecovery = "check that the relay directory is mounted and readable by this user " +
	"(permissions, mount state, a failing disk or network share), then rerun `mtix sync relay tick`; " +
	"the stalled peer resumes from its banked position and the stall clears on the next successful read"

// relayMediumStalls probes every peer's unread segments without applying
// anything, returning the peers stalled by RELAY_MEDIUM_UNREADABLE. Both
// `relay status` and doctor use it, so they cannot disagree.
func relayMediumStalls(ctx context.Context, dir string) ([]ingest.Stall, error) {
	self, err := relayPeerID()
	if err != nil {
		return nil, err
	}
	return ingest.ProbeMedium(filepath.Join(dir, tick.PeersDirName), self,
		func(peer string) (ingest.Position, error) {
			pos, curErr := app.store.RelayIngestCursor(ctx, peer)
			return ingest.Position{SegmentNo: pos.SegmentNo, RS: pos.RS, PubEpoch: pos.PubEpoch}, curErr
		})
}

// checkRelayMedium is the doctor's "relay medium" row: it fails naming each
// stalled peer, the code and the fix. --json carries the same text.
func checkRelayMedium(ctx context.Context, dir string) DoctorCheck {
	row := DoctorCheck{Name: relayMediumRow}
	stalls, err := relayMediumStalls(ctx, dir)
	if err != nil {
		row.Detail = relayDoctorHint(err)
		return row
	}
	if len(stalls) == 0 {
		row.Pass, row.Detail = true, "every peer's unread segments are readable"
		return row
	}
	lines := make([]string, len(stalls))
	for i, s := range stalls {
		lines[i] = fmt.Sprintf("peer %s: %s", oneLine(s.PeerID), oneLine(s.Reason))
	}
	row.Fix = relayMediumRecovery
	row.Detail = fmt.Sprintf("%d peer(s) stalled, the relay medium cannot be read:\n    %s\n    fix: %s",
		len(stalls), strings.Join(lines, "\n    "), relayMediumRecovery)
	return row
}
