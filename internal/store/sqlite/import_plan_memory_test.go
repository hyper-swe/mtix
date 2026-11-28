// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// MTIX-2.3.2.2: planning the reconciliation from the staged rows and writing
// the report and the remap stay within the heap ceiling of slice 1, whatever
// the size of the board, and the in-memory path over the same shape does not.
const (
	planSmallBoard   = 200
	planLargeBoard   = 1600
	planNodeBody     = 16384 // bytes of description per node
	planControlBoard = 40000
)

// planBoardUID is the uid of the i-th incoming node; the root has i = 0.
func planBoardUID(i int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012d", i) }

// writePlanBoard writes an export of one settled root and n provisional children
// of it, all with uids and a 16 KiB description, holding one node at a time.
func writePlanBoard(t *testing.T, path string, n int) int64 {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	w := bufio.NewWriterSize(f, 1<<20)
	_, _ = w.WriteString(`{"version":1,"project":"BIG","nodes":[`)
	filler := strings.Repeat("lorem ipsum dolor sit amet ", planNodeBody/27+1)[:planNodeBody]
	for i := 0; i <= n; i++ {
		node := stageNode("BIG-1", "root")
		node.Project, node.UID, node.Seq = "BIG", planBoardUID(0), 1
		if i > 0 {
			id, idErr := model.BuildProvisionalID("BIG-1", planBoardUID(i))
			require.NoError(t, idErr)
			node = stageNode(id, fmt.Sprintf("node %d", i))
			node.Project, node.UID, node.ParentID, node.Depth, node.Seq = "BIG", planBoardUID(i), "BIG-1", 1, 1
			node.NodeType = "story"
		}
		node.Description = filler
		raw, err := json.Marshal(node)
		require.NoError(t, err)
		if i > 0 {
			_, _ = w.WriteString(",")
		}
		_, _ = w.Write(raw)
	}
	_, _ = w.WriteString(`],"dependencies":[]}`)
	require.NoError(t, w.Flush())
	require.NoError(t, f.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}

// planLocalStore returns a store that holds the board's root.
func planLocalStore(t *testing.T) *Store {
	t.Helper()
	s := newInternalTestStore(t)
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(context.Background(), &model.Node{
		ID: "BIG-1", Project: "BIG", Seq: 1, Title: "root", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeEpic, ContentHash: "h", UID: planBoardUID(0), CreatedAt: now, UpdatedAt: now,
	}))
	return s
}

// plannedPeakHeap stages, plans, and writes the report and the remap of a
// board of n provisional nodes, and returns its size, the peak heap over the
// start, and the number of remaps.
func plannedPeakHeap(t *testing.T, n int) (int64, uint64, int) {
	t.Helper()
	s := planLocalStore(t)
	path := filepath.Join(t.TempDir(), "big.json")
	size := writePlanBoard(t, path, n)
	var remaps int
	remapFile := filepath.Join(t.TempDir(), "remap.json")
	peak := peakHeapOver(func() {
		f, err := os.Open(path)
		require.NoError(t, err)
		defer func() { _ = f.Close() }()
		st, err := StageExport(context.Background(), f, t.TempDir())
		require.NoError(t, err)
		defer func() { require.NoError(t, st.Close()) }()
		plan, err := s.PlanImportStaged(context.Background(), st, ImportReconcileOptions{Mode: ImportModeMerge, Confirm: true})
		require.NoError(t, err)
		remaps = plan.counts[secRemap]
		require.NoError(t, plan.WriteReport(io.Discard))
		out, err := os.Create(remapFile)
		require.NoError(t, err)
		w := bufio.NewWriter(out)
		require.NoError(t, plan.WriteRemap(w))
		require.NoError(t, w.Flush())
		require.NoError(t, out.Close())
	})
	t.Logf("board %d MiB, %d remaps: peak heap over start %d KiB (ceiling %d MiB)", size>>20, remaps, peak>>10, stageHeapCeiling>>20)
	return size, peak, remaps
}

// Two board sizes: both plan and report within the ceiling, and the peak of the
// board eight times larger is not larger by more than stagePeakGrowthLimit.
func TestPlanImportStaged_PeakHeapDoesNotScaleWithBoard(t *testing.T) {
	if testing.Short() {
		t.Skip("generates a large board")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(20))
	_, small, smallRemaps := plannedPeakHeap(t, planSmallBoard)
	size, large, largeRemaps := plannedPeakHeap(t, planLargeBoard)
	assert.Equal(t, planSmallBoard, smallRemaps)
	assert.Equal(t, planLargeBoard, largeRemaps)
	require.Greater(t, size, int64(3*stageHeapCeiling), "the board must dwarf the ceiling")
	assert.Less(t, small, uint64(stageHeapCeiling))
	assert.Less(t, large, uint64(stageHeapCeiling), "planning + report output must not grow with the board")
	assert.Less(t, large, small+stagePeakGrowthLimit, "the peak must not scale with the board")
}

// The control: the report and the remap file as they were built, one entry per
// remapped node in slices and a map, breach the same ceiling, so the ceiling
// above is a bound the streamed output keeps and not slack.
func TestPlanImportStaged_InMemoryReportExceedsTheCeiling(t *testing.T) {
	if testing.Short() {
		t.Skip("generates a large report")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(20))
	peak := peakHeapOver(func() {
		report := &ImportReconcileReport{}
		for i := 1; i <= planControlBoard; i++ {
			uid := planBoardUID(i)
			report.Remaps = append(report.Remaps, ImportRemapEntry{
				UID: uid, OldPath: "BIG-1.x" + uid[len(uid)-12:] + "abcdefgh", NewPath: fmt.Sprintf("BIG-1.%d", i)})
		}
		assert.NotEmpty(t, report.String())
		remap := map[string]string{}
		for _, m := range report.Remaps {
			remap[m.UID] = m.NewPath
		}
		out, err := json.MarshalIndent(remap, "", "  ")
		require.NoError(t, err)
		assert.NotEmpty(t, out)
	})
	t.Logf("in-memory report and remap of %d entries: peak heap over start %d MiB", planControlBoard, peak>>20)
	assert.Greater(t, peak, uint64(stageHeapCeiling), "building the report in memory must breach the ceiling")
}
