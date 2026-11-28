// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package relaygate_test

import (
	"fmt"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/stretchr/testify/require"
)

// The kill harness (errKilled, killStore, survive, killedPublishKeeping) lives
// in publisher_kill_harness_test.go (MTIX-301); these tests reuse it.

var headerCuts = []int64{0, 1, segment.HeaderSize / 2, segment.HeaderSize - 1}

// TestRelayGate_PublisherKilledMidSegmentHeader_RestartRecovers kills the
// publisher while its first segment file is created; a restart recovers
// with no operator step and the reader converges once (FR-21 §5.5, MTIX-302).
func TestRelayGate_PublisherKilledMidSegmentHeader_RestartRecovers(t *testing.T) {
	for _, keep := range headerCuts {
		t.Run(fmt.Sprintf("header_bytes_%d", keep), func(t *testing.T) {
			g := newGateRelay(t, publisherID, "fedcba9876543210")
			g.createNodes(t, 0, 1, 1)
			killedPublishKeeping(t, g, 0, func(int64) int64 { return keep })

			require.Equal(t, 1, g.pass(t, 0).Published)
			g.pass(t, 1)
			require.Equal(t, []string{"TEST-1"}, g.storedNodes(t, 1, 1))
			rows, distinct := eventCounts(t, g, 1)
			require.Equal(t, rows, distinct)
		})
	}
}

// TestRelayGate_PublisherKilledMidRotationHeader_RestartRecovers kills the
// publisher while a later segment is created: the reader must not stall on
// the header-only newest segment, and converges once the publisher restarts.
func TestRelayGate_PublisherKilledMidRotationHeader_RestartRecovers(t *testing.T) {
	for _, keep := range headerCuts {
		t.Run(fmt.Sprintf("header_bytes_%d", keep), func(t *testing.T) {
			g := newGateRelay(t, publisherID, "fedcba9876543210")
			g.peers[0].MaxSegmentBytes = segment.HeaderSize + 1 // one record per segment
			g.createNodes(t, 0, 1, 1)
			g.pass(t, 0)
			g.createNodes(t, 0, 2, 2)
			killedPublishKeeping(t, g, 0, func(int64) int64 { return keep })

			res := g.pass(t, 1)                       // reader polls while the publisher is down
			for _, stall := range res.Ingest.Stalls { // a retry note, never a corruption verdict
				require.Empty(t, stall.Code, "a newest header-incomplete segment is not RELAY_SEGMENT_CORRUPT")
			}
			require.Equal(t, []string{"TEST-1"}, g.storedNodes(t, 1, 2))

			require.Equal(t, 1, g.pass(t, 0).Published)
			g.pass(t, 1)
			require.Equal(t, []string{"TEST-1", "TEST-2"}, g.storedNodes(t, 1, 2))
			rows, distinct := eventCounts(t, g, 1)
			require.Equal(t, 2, rows)
			require.Equal(t, rows, distinct)
		})
	}
}
