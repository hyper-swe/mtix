// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package publisher

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hyper-swe/mtix/internal/relay/segment"
)

// RepublishFrom re-emits this peer's events from an earlier relay
// sequence, returning how many were re-sent (FR-21 §6.8).
//
// It is the operator's gap repair after a reader condemned a sealed
// segment. The events are the SAME events — what changed is that a
// reader lost them — so they go out under fresh relay sequences in
// fresh segments, never reusing a consumed position and never touching
// a file that already exists. The duplicates are absorbed on arrival,
// which is what makes the repair safe to run more than once.
//
// The publisher epoch does not move. Bumping it would tell every reader
// a restore had happened when none had, and send them through a
// recovery they do not need.
func (p *Publisher) RepublishFrom(ctx context.Context, fromRS uint64) (int, error) {
	if fromRS == 0 {
		return 0, fmt.Errorf("republish: relay sequence 0 is not a position")
	}
	floor, err := p.journalFloorForRS(ctx, fromRS)
	if err != nil {
		return 0, err
	}
	if err := p.cfg.Journal.RepublishRelayFrom(ctx, floor); err != nil {
		return 0, err
	}
	// The tail this peer already published is what the repair re-sends,
	// so the §5.7 startup check has nothing left to tell us.
	p.verified = true
	// §6.8 re-emits into FRESH segments: every existing file stays
	// byte-identical, so a reader that already read one sees no change
	// under it while the repair arrives alongside.
	p.forceRotate = true
	return p.PublishPending(ctx)
}

// journalFloorForRS finds the journal position just before the event
// this peer published at fromRS.
//
// The mapping is recovered from the medium rather than stored, because
// the medium is where it is authoritative: the record at that relay
// sequence names the event, and the journal names where that event
// sits. Refusing when it cannot be found is deliberate — a guessed
// floor either re-sends nothing useful or re-sends the whole history.
func (p *Publisher) journalFloorForRS(ctx context.Context, fromRS uint64) (int64, error) {
	segs, _, err := segment.ListSegments(p.cfg.SegmentsDir)
	if err != nil {
		return 0, err
	}
	for i, file := range segs {
		res, scanErr := segment.ScanFile(file.Path, segment.ScanOptions{
			Sealed:       i < len(segs)-1,
			Key:          p.cfg.Key,
			ExpectPeerID: p.cfg.PeerID,
		})
		if scanErr != nil {
			continue
		}
		for _, rec := range res.Records {
			if rec.RS != fromRS {
				continue
			}
			var e struct {
				EventID string `json:"event_id"`
			}
			if err := json.Unmarshal(rec.Payload, &e); err != nil || e.EventID == "" {
				return 0, fmt.Errorf("republish: the record at rs %d carries no event id", fromRS)
			}
			seqs, err := p.cfg.Journal.LookupRelayJournalSeqs(ctx, []string{e.EventID})
			if err != nil {
				return 0, err
			}
			seq, ok := seqs[e.EventID]
			if !ok {
				return 0, fmt.Errorf("republish: the event published at rs %d is no longer in the journal", fromRS)
			}
			return seq - 1, nil
		}
	}
	return 0, fmt.Errorf("republish: no record at rs %d in this peer's segments", fromRS)
}
