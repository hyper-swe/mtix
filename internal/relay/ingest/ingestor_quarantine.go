// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest

import (
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// quarantineRecord builds the stored form of a skipped record; e is nil when
// the payload did not decode.
func (in *Ingestor) quarantineRecord(peerID string, header segment.Header, rec segment.Record, e *model.SyncEvent, cause error) sqlite.RelayQuarantineRecord {
	now := time.Now
	if in.cfg.Now != nil {
		now = in.cfg.Now
	}
	row := sqlite.RelayQuarantineRecord{
		RelayQuarantineKey: sqlite.RelayQuarantineKey{PeerID: peerID, PubEpoch: uint64(header.PubEpoch), RS: rec.RS},
		SegmentNo:          header.SegmentNo,
		Reason:             cause.Error(),
		Payload:            rec.Payload,
		SeenAt:             now().UTC().Format(time.RFC3339),
	}
	if e != nil {
		row.EventID, row.NodeID, row.OpType, row.Lamport = e.EventID, e.NodeID, string(e.OpType), e.LamportClock
	}
	return row
}
