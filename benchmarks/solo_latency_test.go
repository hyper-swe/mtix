// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package benchmarks holds the FR-18 / MTIX-15.10 perf suite.
//
// Two test files cover the targets from the requirements:
//
//   - solo_latency_test.go:  solo CLI latency  < 10ms per command.
//   - memory_test.go:        100K-node project < 50MB heap.
//   - pool_config_test.go:   transport.Pool MaxConns <= 5.
//   - sync_throughput_test.go (15.10.1): sync push/pull of 1000 events < 5s.
//
// Tests run as part of `go test ./...` when targets are achievable
// without external services. PG-bound benchmarks gate on
// MTIX_PG_TEST_DSN like the e2e suite.
//
// Failure messages always include the OBSERVED time / memory so
// regressions are diagnosable, not just "exceeded threshold".

package benchmarks

import (
	"context"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// soloLatencyTarget is the FR-18 / MTIX-15.10 per-command ceiling
// for solo (no-PG) operations. Median across 100 iterations.
const soloLatencyTarget = 10 * time.Millisecond

// newSoloStore opens a sqlite.Store in t.TempDir and registers cleanup.
func newSoloStore(t testing.TB) *sqlite.Store {
	t.Helper()
	dir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := sqlite.New(dir, logger)
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// makeNode constructs a model.Node with the given id, project derived
// from the id prefix.
func makeNode(id, title string) *model.Node {
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	return &model.Node{
		ID:        id,
		Project:   "PRJ",
		Title:     title,
		Status:    model.StatusOpen,
		Priority:  model.PriorityMedium,
		NodeType:  model.NodeTypeAuto,
		Weight:    1.0,
		Creator:   "perf",
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// summary returns (median, p95) of a duration slice. The slice is
// mutated (sorted) — callers should not rely on the input order.
func summary(durations []time.Duration) (median, p95 time.Duration) {
	sort.Slice(durations, func(i, j int) bool {
		return durations[i] < durations[j]
	})
	n := len(durations)
	if n == 0 {
		return 0, 0
	}
	median = durations[n/2]
	p95Idx := (n * 95) / 100
	if p95Idx >= n {
		p95Idx = n - 1
	}
	p95 = durations[p95Idx]
	return median, p95
}

// TestPerf_SoloCommandTargets enforces the <10ms median target for
// the three operations a solo CLI uses most: create, update, list.
//
// Each operation is run 100 times; assertion uses the median to
// reduce CI-noise sensitivity. The failure message reports both
// median and p95 so regressions are diagnosable.
func TestPerf_SoloCommandTargets(t *testing.T) {
	if testing.Short() {
		t.Skip("perf targets skipped under -short")
	}
	st := newSoloStore(t)
	ctx := context.Background()

	// Pre-create 100 nodes for the update / list paths.
	for i := 0; i < 100; i++ {
		require.NoError(t, st.CreateNode(ctx, makeNode("PRJ-"+strconv.Itoa(i+1), "seed")))
	}

	t.Run("create", func(t *testing.T) {
		const iterations = 100
		durs := make([]time.Duration, iterations)
		for i := 0; i < iterations; i++ {
			n := makeNode("PRJ-CR-"+strconv.Itoa(i+1), "create-perf")
			start := time.Now()
			require.NoError(t, st.CreateNode(ctx, n))
			durs[i] = time.Since(start)
		}
		median, p95 := summary(durs)
		require.LessOrEqualf(t, median, soloLatencyTarget,
			"create median=%s p95=%s; target=%s", median, p95, soloLatencyTarget)
	})

	t.Run("update", func(t *testing.T) {
		const iterations = 100
		durs := make([]time.Duration, iterations)
		title := "updated"
		for i := 0; i < iterations; i++ {
			id := "PRJ-" + strconv.Itoa((i%100)+1)
			start := time.Now()
			require.NoError(t, st.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}))
			durs[i] = time.Since(start)
		}
		median, p95 := summary(durs)
		require.LessOrEqualf(t, median, soloLatencyTarget,
			"update median=%s p95=%s; target=%s", median, p95, soloLatencyTarget)
	})

	t.Run("list", func(t *testing.T) {
		const iterations = 100
		durs := make([]time.Duration, iterations)
		for i := 0; i < iterations; i++ {
			start := time.Now()
			_, _, err := st.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 1000})
			require.NoError(t, err)
			durs[i] = time.Since(start)
		}
		median, p95 := summary(durs)
		require.LessOrEqualf(t, median, soloLatencyTarget,
			"list median=%s p95=%s; target=%s", median, p95, soloLatencyTarget)
	})
}

// BenchmarkSolo_Create_NoSyncOverhead measures store.CreateNode
// throughput for an empty store (event emission included).
func BenchmarkSolo_Create_NoSyncOverhead(b *testing.B) {
	st := newSoloStore(b)
	ctx := context.Background()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		n := makeNode("PRJ-B-"+strconv.Itoa(i+1), "bench")
		if err := st.CreateNode(ctx, n); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSolo_Update_NoSyncOverhead measures store.UpdateNode
// against a 1000-node store.
func BenchmarkSolo_Update_NoSyncOverhead(b *testing.B) {
	st := newSoloStore(b)
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		if err := st.CreateNode(ctx, makeNode("PRJ-"+strconv.Itoa(i+1), "seed")); err != nil {
			b.Fatal(err)
		}
	}
	title := "updated"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		id := "PRJ-" + strconv.Itoa((i%1000)+1)
		if err := st.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSolo_List_NoSyncOverhead measures ListNodes against a
// 1000-node store.
func BenchmarkSolo_List_NoSyncOverhead(b *testing.B) {
	st := newSoloStore(b)
	ctx := context.Background()
	for i := 0; i < 1000; i++ {
		if err := st.CreateNode(ctx, makeNode("PRJ-"+strconv.Itoa(i+1), "seed")); err != nil {
			b.Fatal(err)
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := st.ListNodes(ctx, store.NodeFilter{}, store.ListOptions{Limit: 1000}); err != nil {
			b.Fatal(err)
		}
	}
}
