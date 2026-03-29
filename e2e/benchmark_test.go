// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// benchmarkEnv creates a benchmark environment (not using t.Helper since benchmarks).
func benchmarkEnv(b *testing.B) *e2eEnv {
	b.Helper()

	tmpDir := b.TempDir()
	dbDir := filepath.Join(tmpDir, "data")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		b.Fatal(err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelError,
	}))

	st, err := sqlite.New(dbDir, logger)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = st.Close() })

	broadcaster := &service.NoopBroadcaster{}
	config := &service.StaticConfig{}
	clock := testClock()

	nodeSvc := service.NewNodeService(st, broadcaster, config, logger, clock)
	sessionSvc := service.NewSessionService(st, config, logger, clock)

	return &e2eEnv{
		store:      st,
		sqlStore:   st,
		nodeSvc:    nodeSvc,
		sessionSvc: sessionSvc,
		ctx:        context.Background(),
	}
}

// BenchmarkCreateNode measures node creation overhead per NFR.
// Target: <10ms per node.
func BenchmarkCreateNode(b *testing.B) {
	env := benchmarkEnv(b)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
			Title:   fmt.Sprintf("Benchmark Node %d", i),
			Project: "BENCH",
			Creator: "bench-agent",
		})
		if err != nil {
			b.Fatal(err)
		}
	}

	// Log average time per operation.
	avgNs := b.Elapsed().Nanoseconds() / int64(b.N)
	avgMs := float64(avgNs) / 1e6
	if avgMs > 10.0 {
		b.Errorf("CreateNode avg %.2fms exceeds 10ms target", avgMs)
	}
}

// BenchmarkGetTree_1000 measures 1000-node tree retrieval per NFR.
// Target: <100ms.
func BenchmarkGetTree_1000(b *testing.B) {
	env := benchmarkEnv(b)

	// Build a 1000-node tree: 1 root + 10 epics × 99 issues ≈ 1000.
	root, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Tree Root",
		Project: "TREE",
		Creator: "bench",
	})
	require.NoError(b, err)

	// Create 10 children of root.
	epicInputs := make([]service.DecomposeInput, 10)
	for i := range epicInputs {
		epicInputs[i] = service.DecomposeInput{
			Title: fmt.Sprintf("Epic %d", i+1),
		}
	}
	epicIDs, err := env.nodeSvc.Decompose(env.ctx, root.ID, epicInputs, "bench")
	require.NoError(b, err)

	// Create ~99 issues per epic = 990 issues.
	for _, epicID := range epicIDs {
		issueInputs := make([]service.DecomposeInput, 99)
		for i := range issueInputs {
			issueInputs[i] = service.DecomposeInput{
				Title: fmt.Sprintf("Issue %d", i+1),
			}
		}
		_, err := env.nodeSvc.Decompose(env.ctx, epicID, issueInputs, "bench")
		require.NoError(b, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		tree, err := env.store.GetTree(env.ctx, root.ID, 10)
		if err != nil {
			b.Fatal(err)
		}
		if len(tree) < 100 {
			b.Fatalf("expected ≥100 nodes, got %d", len(tree))
		}
	}

	avgNs := b.Elapsed().Nanoseconds() / int64(b.N)
	avgMs := float64(avgNs) / 1e6
	if avgMs > 100.0 {
		b.Errorf("GetTree_1000 avg %.2fms exceeds 100ms target", avgMs)
	}
}

// BenchmarkProgressRollup_Depth10 measures progress rollup on a 10-level tree.
// Target: <50ms.
func BenchmarkProgressRollup_Depth10(b *testing.B) {
	env := benchmarkEnv(b)

	// Build a 10-level deep chain: root → child1 → child2 → ... → child10.
	var parentID string
	root, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Depth Root",
		Project: "DEEP",
		Creator: "bench",
	})
	require.NoError(b, err)
	parentID = root.ID

	var leafID string
	for depth := 1; depth <= 10; depth++ {
		ids, err := env.nodeSvc.Decompose(env.ctx, parentID, []service.DecomposeInput{
			{Title: fmt.Sprintf("Level %d", depth)},
		}, "bench")
		require.NoError(b, err)
		parentID = ids[0]
		leafID = ids[0]
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		// Claim and complete the leaf → triggers rollup up 10 levels.
		err := env.store.ClaimNode(env.ctx, leafID, "bench-agent")
		if err != nil {
			// May already be claimed from previous iteration — unclaim first.
			_ = env.store.UnclaimNode(env.ctx, leafID, "reset", "bench")
			_ = env.store.ClaimNode(env.ctx, leafID, "bench-agent")
		}
		_ = env.nodeSvc.TransitionStatus(env.ctx, leafID, model.StatusDone,
			"done", "bench-agent")

		// Reset for next iteration.
		_ = env.nodeSvc.TransitionStatus(env.ctx, leafID, model.StatusOpen,
			"reset", "bench")
	}

	avgNs := b.Elapsed().Nanoseconds() / int64(b.N)
	avgMs := float64(avgNs) / 1e6
	if avgMs > 50.0 {
		b.Errorf("ProgressRollup_Depth10 avg %.2fms exceeds 50ms target", avgMs)
	}
}

// BenchmarkProgressRollup_Depth50 measures progress rollup on a 50-level tree.
// Target: <250ms (advisory).
func BenchmarkProgressRollup_Depth50(b *testing.B) {
	env := benchmarkEnv(b)

	var parentID string
	root, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "Deep Root",
		Project: "D50",
		Creator: "bench",
	})
	require.NoError(b, err)
	parentID = root.ID

	var leafID string
	for depth := 1; depth <= 50; depth++ {
		ids, err := env.nodeSvc.Decompose(env.ctx, parentID, []service.DecomposeInput{
			{Title: fmt.Sprintf("Level %d", depth)},
		}, "bench")
		require.NoError(b, err)
		parentID = ids[0]
		leafID = ids[0]
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		err := env.store.ClaimNode(env.ctx, leafID, "bench-agent")
		if err != nil {
			_ = env.store.UnclaimNode(env.ctx, leafID, "reset", "bench")
			_ = env.store.ClaimNode(env.ctx, leafID, "bench-agent")
		}
		_ = env.nodeSvc.TransitionStatus(env.ctx, leafID, model.StatusDone,
			"done", "bench-agent")

		_ = env.nodeSvc.TransitionStatus(env.ctx, leafID, model.StatusOpen,
			"reset", "bench")
	}

	avgNs := b.Elapsed().Nanoseconds() / int64(b.N)
	avgMs := float64(avgNs) / 1e6
	if avgMs > 250.0 {
		b.Logf("WARNING: ProgressRollup_Depth50 avg %.2fms exceeds 250ms advisory limit", avgMs)
	}
}

// BenchmarkFTSSearch_10K measures FTS search over 10,000 nodes per NFR.
// Target: <50ms.
func BenchmarkFTSSearch_10K(b *testing.B) {
	env := benchmarkEnv(b)

	// Bulk create 10,000 nodes.
	root, err := env.nodeSvc.CreateNode(env.ctx, &service.CreateNodeRequest{
		Title:   "FTS Root",
		Project: "FTS",
		Creator: "bench",
	})
	require.NoError(b, err)

	// Create in batches of 100.
	for batch := 0; batch < 100; batch++ {
		inputs := make([]service.DecomposeInput, 100)
		for i := range inputs {
			inputs[i] = service.DecomposeInput{
				Title: fmt.Sprintf("Searchable node batch %d item %d authentication", batch, i),
			}
		}
		_, err := env.nodeSvc.Decompose(env.ctx, root.ID, inputs, "bench")
		require.NoError(b, err)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		results, _, err := env.store.SearchNodes(env.ctx, "authentication",
			store.NodeFilter{}, store.ListOptions{Limit: 20})
		if err != nil {
			b.Fatal(err)
		}
		if len(results) == 0 {
			b.Fatal("FTS returned 0 results")
		}
	}

	avgNs := b.Elapsed().Nanoseconds() / int64(b.N)
	avgMs := float64(avgNs) / 1e6
	if avgMs > 50.0 {
		b.Errorf("FTSSearch_10K avg %.2fms exceeds 50ms target", avgMs)
	}
}
