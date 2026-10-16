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

const (
	// historyBoardSize is the large board used to prove the recorder does not scan nodes.
	historyBoardSize = 100000
	// historySmallBoardSize is the reference board the large board is compared with.
	historySmallBoardSize = 1000
	// historyScaleRatio is the most the median write cost may grow from the
	// small board to the large one. B-tree depth adds well under 2x for a 100x
	// larger board; a per-write scan of the nodes table adds about 100x.
	historyScaleRatio = 8.0
	historySamples    = 41
)

// TestTaskHistory_LargeBoard_WriteCostDoesNotGrowWithBoardSize proves UpdateNode
// and CreateNode keep their cost when the board grows 100x (NFR-1.1, F2). It
// compares the two boards in one process, with samples interleaved, so load on
// the machine and the race detector slow both sides alike and cancel out.
func TestTaskHistory_LargeBoard_WriteCostDoesNotGrowWithBoardSize(t *testing.T) {
	if testing.Short() {
		t.Skip("seeds a 100k-task board")
	}
	ctx := context.Background()
	small := newScaleBoard(t, "SML", historySmallBoardSize)
	large := newScaleBoard(t, "BIG", historyBoardSize)

	updateOp := func(b *scaleBoard) func(i int) error {
		return func(i int) error {
			title := fmt.Sprintf("renamed %d", i)
			id := fmt.Sprintf("%s-%d", b.project, b.size/2+i)
			return b.f.s.UpdateNode(ctx, id, &store.NodeUpdate{Title: &title})
		}
	}
	createOp := func(b *scaleBoard) func(i int) error {
		return func(i int) error {
			seq := b.size + 1 + i
			n := seqNode(fmt.Sprintf("%s-%d", b.project, seq), seq)
			n.Project = b.project
			return b.f.s.CreateNode(ctx, n)
		}
	}

	smallUpdate, largeUpdate := interleavedMedians(t, historySamples, updateOp(small), updateOp(large))
	smallCreate, largeCreate := interleavedMedians(t, historySamples, createOp(small), createOp(large))
	t.Logf("UpdateNode median: %d-task board %v, %d-task board %v", small.size, smallUpdate, large.size, largeUpdate)
	t.Logf("CreateNode median: %d-task board %v, %d-task board %v", small.size, smallCreate, large.size, largeCreate)
	requireWithinRatio(t, "UpdateNode", smallUpdate, largeUpdate)
	requireWithinRatio(t, "CreateNode", smallCreate, largeCreate)
}

type scaleBoard struct {
	f       *historyFixture
	project string
	size    int
}

func newScaleBoard(t *testing.T, project string, size int) *scaleBoard {
	t.Helper()
	f := newHistoryFixture(t)
	_, err := f.db.Exec(`WITH RECURSIVE c(i) AS (SELECT 1 UNION ALL SELECT i+1 FROM c WHERE i < ?)
		INSERT INTO nodes (id, parent_id, depth, seq, project, title, created_at, updated_at, uid)
		SELECT ? || '-' || i, NULL, 0, i, ?, 't' || i, '2026-10-13T09:00:00Z', '2026-10-13T09:00:00Z', ? || '-uid-' || i FROM c`,
		size, project, project, project)
	require.NoError(t, err)
	return &scaleBoard{f: f, project: project, size: size}
}

func requireWithinRatio(t *testing.T, name string, small, large time.Duration) {
	t.Helper()
	limit := time.Duration(float64(small) * historyScaleRatio)
	require.LessOrEqual(t, large, limit,
		"%s median grew from %v to %v, more than %.0fx, when the board grew %dx", name, small, large, historyScaleRatio, historyBoardSize/historySmallBoardSize)
}

// interleavedMedians runs a and b alternately n times each and returns the median latency of each.
func interleavedMedians(t *testing.T, n int, a, b func(i int) error) (time.Duration, time.Duration) {
	t.Helper()
	da := make([]time.Duration, 0, n)
	db := make([]time.Duration, 0, n)
	for i := 0; i < n; i++ {
		da = append(da, timeOp(t, a, i))
		db = append(db, timeOp(t, b, i))
	}
	return median(da), median(db)
}

func timeOp(t *testing.T, op func(i int) error, i int) time.Duration {
	t.Helper()
	start := time.Now()
	require.NoError(t, op(i))
	return time.Since(start)
}

func median(ds []time.Duration) time.Duration {
	sort.Slice(ds, func(a, b int) bool { return ds[a] < ds[b] })
	return ds[len(ds)/2]
}
