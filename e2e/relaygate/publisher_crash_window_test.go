// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package relaygate_test

import (
	"fmt"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// TestRelayGate_PublisherKilledAfterAppendBeforeCursor_RestartResumes is the
// ordinary §6.2 crash window: the frames reached the medium, the cursor did
// not move. A restart must resume with no operator step, every event on the
// medium exactly once, and the reader converge (FR-21 §6.2 and §11.2,
// MTIX-301; scenario from MTIX-64.9.1).
func TestRelayGate_PublisherKilledAfterAppendBeforeCursor_RestartResumes(t *testing.T) {
	for _, batch := range []int{1, 2, 3} {
		t.Run(fmt.Sprintf("batch_%d", batch), func(t *testing.T) {
			g := newGateRelay(t, publisherID, "fedcba9876543210")
			g.createNodes(t, 0, 1, 1)
			g.pass(t, 0)
			g.createNodes(t, 0, 2, 1+batch)
			killedPublishKeeping(t, g, 0, func(newBytes int64) int64 { return newBytes })

			g.pass(t, 0) // restart: must not fail RELAY_PEER_ID_CONFLICT
			g.pass(t, 1)
			g.createNodes(t, 0, 2+batch, 2+batch)
			g.pass(t, 0)
			g.pass(t, 1)

			want := make([]string, 0, 2+batch)
			for n := 1; n <= 2+batch; n++ {
				want = append(want, fmt.Sprintf("TEST-%d", n))
			}
			require.Equal(t, want, g.storedNodes(t, 1, 2+batch))
			rows, distinct := eventCounts(t, g, 1)
			require.Equal(t, 2+batch, rows)
			require.Equal(t, rows, distinct)
			require.Equal(t, uint64(2+batch), mediumRecords(t, g, 0), "each event published exactly once")
		})
	}
}

// mediumRecords counts the records on peer i's medium.
func mediumRecords(t *testing.T, g gateRelay, i int) uint64 {
	t.Helper()
	var n uint64
	for k, f := range g.segments(t, i) {
		res, err := segment.ScanFile(f.Path, segment.ScanOptions{
			Sealed: k < len(g.segments(t, i))-1, Key: g.key, ExpectPeerID: g.peers[i].PeerID})
		require.NoError(t, err)
		n += uint64(len(res.Records))
	}
	return n
}
