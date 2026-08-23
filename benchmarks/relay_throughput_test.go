// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// FR-21 §9 performance targets for the file relay transport.
//
//   - Publish: <=1 ms/event p50 for the local append cost. The medium is
//     excluded by design — a bridge mount's propagation is not ours to
//     promise, and correctness never depends on it being fast.
//   - Ingest: per tick, one directory listing per peer and one file open
//     per UNREAD segment. Total tick I/O is O(peers + new records) and
//     never O(history).
//
// Both targets are asserted by ordinary Test functions as well as
// measured by Benchmarks, because CI runs `go test` without -bench: a
// benchmark alone would never fail a build. The Benchmarks are for
// diagnosing a regression the Tests catch.
//
// CALIBRATION NOTE: relayPublishBudget below is a deliberately loose
// local-development ceiling, not the §9 number. The p50 target is
// calibrated once on the release-gate rig, where the constant is
// tightened to the measured value plus headroom; from then on drift
// fails CI. Until that calibration runs, this guards against
// order-of-magnitude regressions only — a publish path that quietly
// became 50x slower would still be caught, a 20% drift would not.
package benchmarks

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/relay/ingest"
	"github.com/hyper-swe/mtix/internal/relay/publisher"
	"github.com/hyper-swe/mtix/internal/relay/segment"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

const (
	// relayPublishBudget is the per-event local append ceiling. See the
	// calibration note above.
	relayPublishBudget = 1 * time.Millisecond

	relayPeerID = "0123456789abcdef"
)

var relayKey = []byte("fr21-benchmark-fixed-key-32-bytes")

// relayRig is a store plus the segment directory its publisher writes.
type relayRig struct {
	store  *sqlite.Store
	segDir string
	made   int
}

func newRelayRig(tb testing.TB) *relayRig {
	tb.Helper()
	root := tb.TempDir()
	st, err := sqlite.New(filepath.Join(root, "relay-bench.db"), slog.New(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))
	require.NoError(tb, err)
	tb.Cleanup(func() { _ = st.Close() })

	segDir := filepath.Join(root, "segments")
	require.NoError(tb, os.MkdirAll(segDir, 0o700))
	return &relayRig{store: st, segDir: segDir}
}

// journal creates n nodes, journalling n events.
func (r *relayRig) journal(tb testing.TB, n int) {
	tb.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < n; i++ {
		r.made++
		id := fmt.Sprintf("PROJ-%d", r.made)
		require.NoError(tb, r.store.CreateNode(ctx, &model.Node{
			ID: id, Project: "PROJ", Depth: 0, Seq: r.made, Title: id,
			Status: model.StatusOpen, Priority: model.PriorityMedium, Weight: 1.0,
			NodeType: model.NodeTypeStory, Creator: "bench",
			ContentHash: model.ComputeContentHash(id, "", "", "", nil),
			CreatedAt:   now, UpdatedAt: now,
		}))
	}
}

func (r *relayRig) publisher(tb testing.TB) *publisher.Publisher {
	tb.Helper()
	p, err := publisher.New(publisher.Config{
		Journal: r.store, SegmentsDir: r.segDir, PeerID: relayPeerID,
		Key: relayKey, KeyEpoch: 1,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	require.NoError(tb, err)
	return p
}

// TestRelayPublish_MeetsTheLatencyTarget asserts the FR-21 §9 publish
// budget. This is the gate CI actually runs.
func TestRelayPublish_MeetsTheLatencyTarget(t *testing.T) {
	const events = 200
	r := newRelayRig(t)
	r.journal(t, events)
	p := r.publisher(t)

	start := time.Now()
	n, err := p.PublishPending(context.Background())
	elapsed := time.Since(start)
	require.NoError(t, err)
	require.Positive(t, n)

	perEvent := elapsed / time.Duration(n)
	require.LessOrEqual(t, perEvent, relayPublishBudget,
		"relay publish is %v/event over %d events (budget %v, total %v)",
		perEvent, n, relayPublishBudget, elapsed)
	t.Logf("relay publish: %v/event over %d events (total %v)", perEvent, n, elapsed)
}

// TestRelayIngest_TickIOIsBoundedByNewWorkNotHistory asserts the other
// half of §9: with a reader caught up, history can grow without bound
// and a poll still opens one segment. A tick that reopened history
// would degrade with age — precisely the failure a long-lived fleet
// would hit last and hardest.
func TestRelayIngest_TickIOIsBoundedByNewWorkNotHistory(t *testing.T) {
	r := newRelayRig(t)
	r.journal(t, 400)

	p, err := publisher.New(publisher.Config{
		Journal: r.store, SegmentsDir: r.segDir, PeerID: relayPeerID,
		Key: relayKey, KeyEpoch: 1, MaxSegmentBytes: 2048,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	require.NoError(t, err)
	_, err = p.PublishPending(context.Background())
	require.NoError(t, err)

	segs, foreign, err := segment.ListSegments(r.segDir)
	require.NoError(t, err)
	require.Empty(t, foreign)
	require.Greater(t, len(segs), 10, "the fixture needs real history to be meaningful")

	// A caught-up reader opens exactly the active tail, however long the
	// history behind it.
	caughtUp := ingest.Position{SegmentNo: segs[len(segs)-1].No, RS: 1, PubEpoch: 1}
	require.Len(t, ingest.Unread(segs, caughtUp), 1,
		"a caught-up poll must open one segment, not %d", len(segs))

	// And a reader one rotation behind opens two, not everything.
	oneBehind := ingest.Position{SegmentNo: segs[len(segs)-2].No, RS: 1, PubEpoch: 1}
	require.Len(t, ingest.Unread(segs, oneBehind), 2)

	t.Logf("relay ingest: %d segments of history, caught-up poll opens 1", len(segs))
}

// BenchmarkRelayPublish measures the per-event local append cost.
func BenchmarkRelayPublish(b *testing.B) {
	r := newRelayRig(b)
	r.journal(b, b.N)
	p := r.publisher(b)

	b.ResetTimer()
	b.ReportAllocs()
	n, err := p.PublishPending(context.Background())
	b.StopTimer()
	require.NoError(b, err)
	require.Positive(b, n)
}

// BenchmarkRelayIngest measures a poll's read cost: one directory
// listing plus a scan of each unread segment, which is what a tick
// actually does.
func BenchmarkRelayIngest(b *testing.B) {
	r := newRelayRig(b)
	r.journal(b, 500)
	p, err := publisher.New(publisher.Config{
		Journal: r.store, SegmentsDir: r.segDir, PeerID: relayPeerID,
		Key: relayKey, KeyEpoch: 1, MaxSegmentBytes: 4096,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	})
	require.NoError(b, err)
	_, err = p.PublishPending(context.Background())
	require.NoError(b, err)

	all, _, err := segment.ListSegments(r.segDir)
	require.NoError(b, err)
	caughtUp := ingest.Position{SegmentNo: all[len(all)-1].No, RS: 0, PubEpoch: 1}

	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		segs, _, listErr := segment.ListSegments(r.segDir)
		if listErr != nil {
			b.Fatal(listErr)
		}
		for _, s := range ingest.Unread(segs, caughtUp) {
			res, scanErr := segment.ScanFile(s.Path, segment.ScanOptions{
				Key: relayKey, ExpectPeerID: relayPeerID,
			})
			if scanErr != nil {
				b.Fatal(scanErr)
			}
			for _, rec := range res.Records {
				var e model.SyncEvent
				if err := json.Unmarshal(rec.Payload, &e); err != nil {
					b.Fatal(err)
				}
			}
		}
	}
}
