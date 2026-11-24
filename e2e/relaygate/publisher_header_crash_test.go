// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package relaygate_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/relay/tick"
	"github.com/stretchr/testify/require"
)

var errKilled = errors.New("process killed (-9 simulation)")

// killStore makes the publisher die after appending, before its cursor
// advance, as a kill -9 would.
type killStore struct{ tick.StackStore }

func (k *killStore) AdvanceRelayPushCursor(context.Context, int64, uint64) error {
	panic(errKilled)
}

// survive runs one pass of a process that is killed: nothing after the
// panic of that process runs.
func survive(t *testing.T, stack *tick.Stack) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			if err, ok := r.(error); ok && errors.Is(err, errKilled) {
				return
			}
			panic(r)
		}
	}()
	_, _ = stack.Pass(t.Context())
}

// killedPublishKeeping publishes peer i's pending events with the publisher
// dying before the cursor advance, then keeps keep(newBytes) bytes of what
// it appended: the file a kill -9 mid-write leaves behind. When the kill
// came while a new segment was created, the whole file counts as new.
func killedPublishKeeping(t *testing.T, g gateRelay, i int, keep func(newBytes int64) int64) {
	t.Helper()
	files := g.segments(t, i)
	var before, beforeNo int64
	if len(files) > 0 {
		before, beforeNo = files[len(files)-1].Size, int64(files[len(files)-1].No)
	}
	cfg := g.peers[i]
	cfg.Store = &killStore{StackStore: cfg.Store}
	stack, err := tick.Open(cfg)
	require.NoError(t, err)
	survive(t, stack)
	files = g.segments(t, i)
	last := files[len(files)-1]
	if int64(last.No) != beforeNo {
		before = 0
	}
	if last.Size > before {
		require.NoError(t, os.Truncate(last.Path, before+keep(last.Size-before)))
	}
}

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

// eventCounts returns the journal rows and distinct event ids on peer i.
func eventCounts(t *testing.T, g gateRelay, i int) (rows, distinct int) {
	t.Helper()
	require.NoError(t, g.sql(i).ReadDB().QueryRowContext(t.Context(),
		`SELECT COUNT(*), COUNT(DISTINCT event_id) FROM sync_events`).Scan(&rows, &distinct))
	return rows, distinct
}
