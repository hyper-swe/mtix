// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
)

// historyBoardSize is the board used to prove the recorder does not scan nodes.
const historyBoardSize = 100000

// TestTaskHistory_LargeBoard_WritesStayWithinBudget proves UpdateNode and
// CreateNode keep their latency on a 100k-task board (NFR-1.1, F2). The
// budget is historyWriteBudget: 10 ms without the race detector, 20 ms with it.
func TestTaskHistory_LargeBoard_WritesStayWithinBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a 100k-task board")
	}
	f := newHistoryFixture(t)
	_, err := f.db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < ?)
		INSERT INTO nodes (id, parent_id, depth, seq, project, title, created_at, updated_at, uid)
		SELECT 'BIG-' || i, NULL, 0, i, 'BIG', 't' || i, '2026-10-13T09:00:00Z', '2026-10-13T09:00:00Z', 'uid-' || i FROM c`, historyBoardSize)
	require.NoError(t, err)
	ctx := context.Background()

	update := medianLatency(t, 21, func(i int) error {
		title := fmt.Sprintf("renamed %d", i)
		return f.s.UpdateNode(ctx, fmt.Sprintf("BIG-%d", 50000+i), &store.NodeUpdate{Title: &title})
	})
	create := medianLatency(t, 21, func(i int) error {
		n := seqNode(fmt.Sprintf("BIG-%d", historyBoardSize+1+i), historyBoardSize+1+i)
		n.Project = "BIG"
		return f.s.CreateNode(ctx, n)
	})
	t.Logf("100k board: UpdateNode median %v, CreateNode median %v", update, create)
	require.Less(t, update, historyWriteBudget, "UpdateNode median")
	require.Less(t, create, historyWriteBudget, "CreateNode median")
}

func medianLatency(t *testing.T, n int, op func(i int) error) time.Duration {
	t.Helper()
	ds := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		start := time.Now()
		require.NoError(t, op(i))
		ds = append(ds, time.Since(start))
	}
	sort.Slice(ds, func(a, b int) bool { return ds[a] < ds[b] })
	return ds[n/2]
}
