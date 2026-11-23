// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package ingest_test

import (
	"context"
	"encoding/binary"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// midFileDamage publishes three records, enlarges the length field of the
// second (so it claims bytes past the end of the file, with the whole third
// record after it) and publishes a successor that overlaps from rs 2. This is
// damage inside the sealed segment, not a torn append (FR-21 5.5).
func midFileDamage(t *testing.T, r *peerRig) {
	t.Helper()
	records := marshalEvents(t,
		event(t, model.OpCreateNode, "PROJ-1", 1, &model.CreateNodePayload{Title: "one"}),
		event(t, model.OpCreateNode, "PROJ-2", 2, &model.CreateNodePayload{Title: "two"}),
		event(t, model.OpCreateNode, "PROJ-3", 3, &model.CreateNodePayload{Title: "three"}))
	r.publish(t, fleetKey, records...)
	raw, err := os.ReadFile(r.segPath(1))
	require.NoError(t, err)
	lenAt := segment.HeaderSize + segment.RecordHeaderSize + len(records[0]) + 4
	binary.BigEndian.PutUint32(raw[lenAt:], binary.BigEndian.Uint32(raw[lenAt:])+1000)
	require.NoError(t, os.WriteFile(r.segPath(1), raw, 0o600))
	r.rs = 2
	r.publish(t, fleetKey, records[1:]...)
}

func TestRelayIngest_MidFileLengthDamageIsNotExcused(t *testing.T) {
	r := newPeerRig(t)
	midFileDamage(t, r)
	in, err := ingest.New(r.config())
	require.NoError(t, err)
	for range 2 {
		stats, err := in.IngestAll(context.Background())
		require.NoError(t, err)
		require.Len(t, stats.Stalls, 1)
		require.Equal(t, segment.CodeSegmentCorrupt, stats.Stalls[0].Code)
		require.Equal(t, uint64(1), stats.Stalls[0].Segment)
		require.Zero(t, r.cursor(t).SegmentNo)
	}
}

// The doctor walk must agree with ingest on every unexplained torn seal and on
// damage that is not a torn tail: each leaves segment 1 unchecked.
func TestFindGaps_UnexplainedTornSealIsReported(t *testing.T) {
	for _, tc := range []struct {
		name           string
		segment, first uint64
		epoch          uint16
		empty, badKey  bool
		wholeFrameMAC  bool
		midFileLength  bool
	}{
		{name: "missing sequence", segment: 2, first: 4, epoch: 1},
		{name: "missing segment", segment: 3, first: 3, epoch: 1},
		{name: "changed epoch", segment: 2, first: 3, epoch: 2},
		{name: "wrong successor MAC", segment: 2, first: 3, epoch: 1, badKey: true},
		{name: "header only", segment: 2, first: 3, epoch: 1, empty: true},
		{name: "whole frame MAC damage", wholeFrameMAC: true},
		{name: "mid-file length damage", midFileLength: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newPeerRig(t)
			switch {
			case tc.midFileLength:
				midFileDamage(t, r)
			case tc.wholeFrameMAC:
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
			default:
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
			}
			rep := r.doctorReport(t)
			require.Empty(t, rep.Gaps)
			require.Len(t, rep.Unchecked, 1)
			require.Contains(t, rep.Unchecked[0].Reason, "segment 1")
		})
	}
}
