// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package benchmarks

import (
	"context"
	"os"
	"runtime"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
)

// memoryTarget100K is the FR-18 / MTIX-15.10 ceiling: HeapAlloc
// after a 100K-node project is loaded into the SQLite store.
const memoryTarget100K = 50 * 1024 * 1024 // 50MB

// envPerfLong gates the slow 100K-node tests. Set
// MTIX_PERF_LONG=1 to enable. Default-skip is the only way to keep
// CI under the 10-min timeout when running with `-race` — on a
// GitHub-hosted runner the 100K insertion + race-detector overhead
// blows past the timeout. Developers can opt in locally.
const envPerfLong = "MTIX_PERF_LONG"

// TestPerf_Memory_100KNodes inserts 100K nodes via the production
// CreateNode path and asserts HeapAlloc stays under 50MB after a
// forced GC. Insertion can take multiple seconds; the assertion
// covers steady-state memory, not insert throughput.
//
// Default-skip — set MTIX_PERF_LONG=1 to enable. Also skipped
// under -short for the same reason.
func TestPerf_Memory_100KNodes(t *testing.T) {
	if testing.Short() {
		t.Skip("100K-node memory test skipped under -short")
	}
	if os.Getenv(envPerfLong) != "1" {
		t.Skipf("100K-node memory test gated behind %s=1 (slow under -race)", envPerfLong)
	}

	st := newSoloStore(t)
	ctx := context.Background()

	const n = 100_000
	for i := 0; i < n; i++ {
		if err := st.CreateNode(ctx, makeNode("PRJ-"+strconv.Itoa(i+1), "node")); err != nil {
			t.Fatalf("insert %d failed: %v", i+1, err)
		}
	}

	// Force two GC cycles to release transient insert allocations.
	// One cycle is sometimes not enough; finalizers may schedule
	// follow-up work.
	runtime.GC()
	runtime.GC()

	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	require.LessOrEqualf(t, ms.HeapAlloc, uint64(memoryTarget100K),
		"100K-node HeapAlloc=%d MB (target %d MB); HeapInuse=%d MB",
		ms.HeapAlloc/(1024*1024),
		memoryTarget100K/(1024*1024),
		ms.HeapInuse/(1024*1024))
}
