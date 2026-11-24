// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// The third frame ends inside its payload. Only the successor has a whole copy.
func tornPrefix(t *testing.T, r *peerRig) [][]byte {
	t.Helper()
	records := marshalEvents(t,
		event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "one"}),
		event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "two"}),
		event(t, model.OpCreateNode, "PROJ-3", 3, &model.CreateNodePayload{Title: "three"}))
	r.publish(t, fleetKey, records...)
	path := r.segPath(1)
	info, err := os.Stat(path)
	require.NoError(t, err)
	require.NoError(t, os.Truncate(path, info.Size()-1))
	return records
}

func TestRelayIngest_TornSealWithOverlap(t *testing.T) {
	ctx := context.Background()
	r := newPeerRig(t)
	records := tornPrefix(t, r)
	r.rs = 2
	r.publish(t, fleetKey, records[1:]...)
	cfg := r.config()
	var log bytes.Buffer
	cfg.Logger = slog.New(slog.NewTextHandler(&log, nil))
	in, err := ingest.New(cfg)
	require.NoError(t, err)
	stats, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Empty(t, stats.Stalls)
	require.Equal(t, 3, stats.Applied)
	require.Zero(t, stats.Quarantined)
	require.Contains(t, log.String(), "reconciled torn seal")
	require.Contains(t, log.String(), "peer="+remotePeer)
	require.Contains(t, log.String(), "segment=1")
	require.Contains(t, log.String(), "successor=2")
	require.Equal(t, uint64(2), r.cursor(t).SegmentNo)
	require.Equal(t, uint64(3), r.cursor(t).RS)
	again, err := in.IngestAll(ctx)
	require.NoError(t, err)
	require.Empty(t, again.Stalls)
	require.Zero(t, again.Applied)
	rows, err := r.store.ReadRelayJournalSince(ctx, 0, 10)
	require.NoError(t, err)
	var ids []string
	for _, row := range rows {
		ids = append(ids, row.Event.NodeID)
	}
	require.Equal(t, []string{"PROJ-1", "PROJ-2", "PROJ-3"}, ids)
}

func TestRelayIngest_UnexplainedTornSeal(t *testing.T) {
	for _, tc := range []struct {
		name           string
		segment, first uint64
		epoch          uint16
		empty, badKey  bool
	}{
		{"missing sequence", 2, 4, 1, false, false},
		{"missing segment", 3, 3, 1, false, false},
		{"changed epoch", 2, 3, 2, false, false},
		{"wrong successor MAC", 2, 3, 1, false, true},
		{"header only", 2, 3, 1, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPeerRig(t)
			records := tornPrefix(t, r)
			r.segNo, r.rs, r.pubEpoch = tc.segment, tc.first, tc.epoch
			tail := records[2:]
			if tc.empty {
				tail = nil
			}
			key := fleetKey
			if tc.badKey {
				key = []byte("different-key")
			}
			r.publish(t, key, tail...)
			in, err := ingest.New(r.config())
			require.NoError(t, err)
			for range 2 {
				stats, err := in.IngestAll(context.Background())
				require.NoError(t, err)
				require.Len(t, stats.Stalls, 1)
				require.Equal(t, segment.CodeSegmentCorrupt, stats.Stalls[0].Code)
				require.Equal(t, uint64(1), stats.Stalls[0].Segment)
				require.Zero(t, r.cursor(t).SegmentNo)
				require.Equal(t, 2, r.journalCount(t))
			}
		})
	}
}

func TestFindGaps_TornSealWithOverlap(t *testing.T) {
	r := newPeerRig(t)
	records := tornPrefix(t, r)
	r.rs = 2
	r.publish(t, fleetKey, records[1:]...)
	unread := func(string) (ingest.Position, error) { return ingest.Position{}, nil }
	rep, err := ingest.FindGaps(context.Background(), r.peersDir, selfPeer, unread, r.config().Keys)
	require.NoError(t, err)
	require.Empty(t, rep.Unchecked)
	require.Empty(t, rep.Gaps)
	require.Zero(t, r.journalCount(t))
}

// Damage inside a sealed segment is not a torn tail: a whole frame with a bad
// MAC stays RELAY_SEGMENT_CORRUPT even when a valid successor overlaps it.
func TestRelayIngest_WholeFrameDamageIsNotExcused(t *testing.T) {
	r := newPeerRig(t)
	records := marshalEvents(t,
		event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "one"}),
		event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "two"}))
	r.publish(t, fleetKey, records...)
	raw, err := os.ReadFile(r.segPath(1))
	require.NoError(t, err)
	raw[len(raw)-1] ^= 0xff
	require.NoError(t, os.WriteFile(r.segPath(1), raw, 0o600))
	r.rs = 2
	r.publish(t, fleetKey, records[1:]...)
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	stats, err := in.IngestAll(context.Background())
	require.NoError(t, err)
	require.Len(t, stats.Stalls, 1)
	require.Equal(t, uint64(1), stats.Stalls[0].Segment)
	require.Equal(t, 1, r.journalCount(t))
}
