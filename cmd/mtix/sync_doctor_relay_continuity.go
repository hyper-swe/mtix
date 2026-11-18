// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/keyring"
	"github.com/hyper-swe/mtix/internal/relay/metadata"
	"github.com/hyper-swe/mtix/internal/relay/tick"
)

// relayContinuityRow is the name of the doctor's continuity row.
const relayContinuityRow = "relay continuity"

// relayContinuityUnchecked is the fix line of a continuity walk that could not
// reach a peer's newest segment: the poll names the stall and its recovery.
const relayContinuityUnchecked = "run `mtix sync relay tick`, which names the stalled peer, its code and the recovery; " +
	"a hole beyond the segment that cannot be read is not ruled out until it reads, then run `mtix sync doctor` again"

// checkRelayContinuity is the doctor's "relay continuity" row (MTIX-265): a
// peer whose stream has a hole (a missing segment, or a jump in relay sequence)
// is not ingested past it, so the rest of its events never arrive. The check is
// local, walks the segments with the same chain ingest uses and applies nothing.
// A failing row names each peer and prints the exact recovery. A walk that
// stopped early (an unreadable segment, a missing key) is a WARN, never a pass:
// a hole beyond it is not ruled out.
func checkRelayContinuity(ctx context.Context, dir string, doc *metadata.Relay) DoctorCheck {
	row := DoctorCheck{Name: relayContinuityRow}
	self, err := relayPeerID()
	if err != nil {
		row.Detail = relayDoctorHint(err)
		return row
	}
	var keys ingest.KeySelector
	if doc.Authenticated {
		ring, ringErr := keyring.Load(relayKeysDir())
		if ringErr != nil {
			row.Pass, row.Warn, row.Fix = true, true, "fix the relay key row first, then run `mtix sync doctor` again"
			row.Detail = "not checked: the relay keys are not readable (see the relay key row)"
			return row
		}
		keys = ring
	}
	rep, err := ingest.FindGaps(ctx, filepath.Join(dir, tick.PeersDirName), self, func(peer string) (ingest.Position, error) {
		pos, curErr := app.store.RelayIngestCursor(ctx, peer)
		if curErr != nil {
			return ingest.Position{}, curErr
		}
		stamp, stampErr := app.store.RelayCloneStampEpoch(ctx, peer)
		cur := ingest.Position{SegmentNo: pos.SegmentNo, RS: pos.RS, PubEpoch: pos.PubEpoch}
		return ingest.StampedPosition(cur, stamp), stampErr
	}, keys)
	if err != nil {
		row.Detail = relayDoctorHint(err)
		return row
	}
	return continuityRow(row, rep)
}

// continuityRow renders a walk's findings: a hole fails, an unfinished walk
// warns, and only a complete clean walk passes.
func continuityRow(row DoctorCheck, rep ingest.GapReport) DoctorCheck {
	if len(rep.Gaps) > 0 {
		lines := make([]string, len(rep.Gaps))
		for i, g := range rep.Gaps {
			lines[i] = g.Reason
		}
		row.Fix = relayGapRecovery
		row.Detail = fmt.Sprintf("%d peer(s) have a hole in their stream and are not ingested past it:\n    %s\n    fix: %s",
			len(rep.Gaps), strings.Join(lines, "\n    "), relayGapRecovery)
		if len(rep.Unchecked) > 0 {
			row.Detail += "\n    not fully checked, a hole beyond is not ruled out:\n    " + uncheckedLines(rep.Unchecked)
		}
		return row
	}
	row.Pass = true
	if len(rep.Unchecked) == 0 {
		row.Detail = "every peer's segments are contiguous from this peer's position"
		return row
	}
	lines := uncheckedLines(rep.Unchecked)
	row.Warn, row.Fix = true, relayContinuityUnchecked
	row.Detail = fmt.Sprintf("not fully checked: %d peer(s) could not be read to their newest segment, so a hole beyond is not ruled out:\n    %s\n    fix: %s",
		len(rep.Unchecked), lines, relayContinuityUnchecked)
	return row
}

// uncheckedLines renders the peers a walk could not read to their newest
// segment, one per line.
func uncheckedLines(un []ingest.Unchecked) string {
	lines := make([]string, len(un))
	for i, u := range un {
		lines[i] = "peer " + u.PeerID + ": " + u.Reason
	}
	return strings.Join(lines, "\n    ")
}
