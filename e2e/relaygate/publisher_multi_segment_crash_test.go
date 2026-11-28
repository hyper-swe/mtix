// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package relaygate_test

import (
	"fmt"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRelayGate_PublisherKilledMidBatch_OneRecordPerSegment_RestartResumes is
// the multi-segment variant of the §6.2 crash window (FR-21, MTIX-301, from
// the MTIX-64.9.3 fault repro). With max_segment_bytes=1 every record of a
// batch seals its own segment, and the publisher advances the push cursor
// once after the whole batch. A kill after only some of the segments landed
// leaves the cursor behind the medium by that many records. The restart must
// resume with no operator step: no RELAY_PEER_ID_CONFLICT, no gap, and every
// event on the medium exactly once.
//
// The mid-batch kill is simulated: the whole batch is appended, then the later
// segments are deleted, as the medium would never have received them. A real
// kill during rotation can also leave a header-only or 0-byte newest segment;
// that case belongs to MTIX-302 and is not covered here.
func TestRelayGate_PublisherKilledMidBatch_OneRecordPerSegment_RestartResumes(t *testing.T) {
	const batch = 4
	for landed := 1; landed < batch; landed++ {
		t.Run(fmt.Sprintf("segments_landed_%d_of_%d", landed, batch), func(t *testing.T) {
			g := newGateRelay(t, publisherID, "fedcba9876543210")
			g.peers[0].MaxSegmentBytes = 1
			g.createNodes(t, 0, 1, 1)
			g.pass(t, 0)
			g.createNodes(t, 0, 2, 1+batch)
			before := len(g.segments(t, 0))
			killedPublishKeeping(t, g, 0, func(newBytes int64) int64 { return newBytes })
			// The kill came after only `landed` of the batch's segments: remove
			// the later ones, as the medium would never have received them.
			files := g.segments(t, 0)
			require.Len(t, files, before+batch, "one sealed segment per record")
			for _, f := range files[before+landed:] {
				require.NoError(t, os.Remove(f.Path))
			}

			g.pass(t, 0) // restart: must not fail RELAY_PEER_ID_CONFLICT
			g.pass(t, 1)
			g.createNodes(t, 0, 2+batch, 2+batch)
			g.pass(t, 0)
			g.pass(t, 1)

			total := 2 + batch
			want := make([]string, 0, total)
			for n := 1; n <= total; n++ {
				want = append(want, fmt.Sprintf("TEST-%d", n))
			}
			require.Equal(t, want, g.storedNodes(t, 1, total))
			rows, distinct := eventCounts(t, g, 1)
			require.Equal(t, total, rows)
			require.Equal(t, rows, distinct)
			require.Equal(t, uint64(total), mediumRecords(t, g, 0), "each event published exactly once")
		})
	}
}
