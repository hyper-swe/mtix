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
//
// The board is not only provisional nodes: a third of its size is local tasks
// the file numbers differently (the local side renumbers them), local tasks
// whose uid the file replaces (adoptions) and tasks whose workflow values
// differ (conflicts, settled by the caller's choice), so every phase of the
// plan runs over a store that grows with the board, and the local-side phases
// are measured as well as the file-side ones.
const (
	planSmallNodes   = 250   // incoming nodes of the small board
	planLargeNodes   = 2000  // eight times as many
	planSmallEach    = 50    // local tasks of each of the three kinds, small board
	planLargeEach    = 400   // the same, eight times as many
	planNodeBody     = 14336 // bytes of description per incoming node
	planControlBoard = 40000

	// planPeakGrowthLimit is how much the peak may grow from the small to the
	// large board. The peak of both boards is the same 1 to 2 MiB (the page
	// cache and the buffers of the staging database), and runs of the same size
	// differ by about 0.1 MiB, so 384 KiB is several times the noise. Planning
	// costs about 30 ms a node under -race, which is why the large board has
	// 2000 nodes and not 16000: between the boards the file side grows by 1750
	// nodes and the local side by 1050, so the limit is about 225 bytes per
	// incoming node or 375 per local node, and state kept per node at 1 KiB
	// breaches it by a factor of 3 to 4 on either side.
	planPeakGrowthLimit = 384 << 10

	planCreatedLocal = "2026-01-01T00:00:00Z" // the local tasks, and the file's where they agree
	planCreatedFile  = "2026-02-01T00:00:00Z" // the file's tasks that are not the local ones
	planRenumberSeq  = 100000                 // BIG-100000.. the file numbers differently from the store
	planWorkflowSeq  = 200000                 // BIG-200000.. the same task, other workflow values
	planAdoptedSeq   = 300000                 // BIG-300000.. the same task, another uid
)

// planBulkLocalSQL inserts a run of local top-level tasks of the board's
// project in one statement: the id and number from ?2, the title prefix ?3, the
// creation time ?4 and the uid from ?5, for ?1 + 1 tasks.
const planBulkLocalSQL = `WITH RECURSIVE n(i) AS (SELECT 0 UNION ALL SELECT i + 1 FROM n WHERE i < ?1)
	INSERT INTO nodes (id, parent_id, depth, seq, project, title, node_type, priority, status, progress, weight,
	  created_at, updated_at, uid, content_hash)
	SELECT 'BIG-' || (?2 + i), NULL, 0, ?2 + i, 'BIG', ?3 || i, 'epic', 3, 'open', 0, 1, ?4, ?4,
	  printf('01900000-0000-7000-8000-%012d', ?5 + i), 'h' FROM n`

// planBoard is the size of a generated board: incoming nodes in all, and local
// tasks of each of the three kinds.
type planBoard struct{ nodes, each int }

// provisional is how many incoming nodes are provisional children of the root.
func (b planBoard) provisional() int { return b.nodes - 1 - 3*b.each }

// planBoardUID is the uid of the i-th incoming node; the root has i = 0.
func planBoardUID(i int) string { return fmt.Sprintf("01900000-0000-7000-8000-%012d", i) }

// planTopNode is an incoming top-level task of the board with the given number.
func planTopNode(seq int, title, uid string) exportNode {
	n := stageNode(fmt.Sprintf("BIG-%d", seq), title)
	n.Project, n.UID, n.Seq, n.CreatedAt = "BIG", uid, seq, planCreatedLocal
	return n
}

// planKindNodes returns the incoming tasks number i of the three kinds the
// local store holds under the same ids: another task (the local one is
// renumbered), the same task with another status (a workflow conflict), and the
// same task under a backfill uid (the file's uid is adopted).
func planKindNodes(t *testing.T, i int) [3]exportNode {
	t.Helper()
	renumber := planTopNode(planRenumberSeq+i, fmt.Sprintf("file renumber %d", i), planBoardUID(1_000_000+i))
	renumber.CreatedAt = planCreatedFile
	workflow := planTopNode(planWorkflowSeq+i, fmt.Sprintf("workflow %d", i), planBoardUID(3_000_000+i))
	workflow.Status = "in_progress"
	backfill, err := model.NewBackfillUID()
	require.NoError(t, err)
	return [3]exportNode{renumber, workflow, planTopNode(planAdoptedSeq+i, fmt.Sprintf("adopted %d", i), backfill)}
}

// writePlanBoard writes an export of the board to path holding one node at a
// time, and returns its size.
func writePlanBoard(t *testing.T, path string, b planBoard) int64 {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	w := bufio.NewWriterSize(f, 1<<20)
	_, _ = w.WriteString(`{"version":1,"project":"BIG","nodes":[`)
	filler := strings.Repeat("lorem ipsum dolor sit amet ", planNodeBody/27+1)[:planNodeBody]
	first := true
	emit := func(n exportNode) {
		n.Description = filler
		raw, marshalErr := json.Marshal(n)
		require.NoError(t, marshalErr)
		if !first {
			_, _ = w.WriteString(",")
		}
		first = false
		_, _ = w.Write(raw)
	}
	root := planTopNode(1, "root", planBoardUID(0))
	emit(root)
	for i := 0; i < b.each; i++ {
		for _, n := range planKindNodes(t, i) {
			emit(n)
		}
	}
	for i := 1; i <= b.provisional(); i++ {
		id, idErr := model.BuildProvisionalID("BIG-1", planBoardUID(i))
		require.NoError(t, idErr)
		n := stageNode(id, fmt.Sprintf("node %d", i))
		n.Project, n.UID, n.ParentID, n.Depth, n.Seq, n.NodeType = "BIG", planBoardUID(i), "BIG-1", 1, 1, "story"
		emit(n)
	}
	_, _ = w.WriteString(`],"dependencies":[]}`)
	require.NoError(t, w.Flush())
	require.NoError(t, f.Close())
	info, err := os.Stat(path)
	require.NoError(t, err)
	return info.Size()
}

// planLocalStore returns a store that holds the board's root and, of each of
// the three kinds, each local tasks that the board's file meets.
func planLocalStore(t *testing.T, each int) *Store {
	t.Helper()
	s := newInternalTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	require.NoError(t, s.CreateNode(ctx, &model.Node{
		ID: "BIG-1", Project: "BIG", Seq: 1, Title: "root", Status: model.StatusOpen, Priority: model.PriorityMedium,
		Weight: 1.0, NodeType: model.NodeTypeEpic, ContentHash: "h", UID: planBoardUID(0), CreatedAt: now, UpdatedAt: now,
	}))
	if each == 0 {
		return s
	}
	for _, kind := range []struct {
		seq     int
		title   string
		uidBase int
	}{
		{planRenumberSeq, "local renumber ", 2_000_000},
		{planWorkflowSeq, "workflow ", 3_000_000},
		{planAdoptedSeq, "adopted ", 4_000_000},
	} {
		_, err := s.writeDB.ExecContext(ctx, planBulkLocalSQL, each-1, kind.seq, kind.title, planCreatedLocal, kind.uidBase)
		require.NoError(t, err)
	}
	return s
}

// planMemoryOptions is the merge the board is planned with: confirmed, and
// every workflow conflict settled for the file.
func planMemoryOptions() ImportReconcileOptions {
	return ImportReconcileOptions{Mode: ImportModeMerge, Confirm: true,
		Workflow: WorkflowResolution{Prefer: WorkflowTheirs}}
}

// planRun is what one measured run of a board returns.
type planRun struct {
	size   int64
	peak   uint64
	counts [secCount]int
}

// stagePlanBoard stages the board file at path and plans it against s.
func stagePlanBoard(t *testing.T, s *Store, path string) (*StagedExport, *ImportPlan) {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	st, err := StageExport(context.Background(), f, t.TempDir())
	require.NoError(t, err)
	plan, err := s.PlanImportStaged(context.Background(), st, planMemoryOptions())
	require.NoError(t, err)
	return st, plan
}

// plannedPeakHeap stages, plans, and writes the report and the remap of the
// board, and returns its size, the peak heap over the start and the counts of
// what the plan decided.
func plannedPeakHeap(t *testing.T, b planBoard) planRun {
	t.Helper()
	s := planLocalStore(t, b.each)
	path := filepath.Join(t.TempDir(), "big.json")
	run := planRun{size: writePlanBoard(t, path, b)}
	remapFile := filepath.Join(t.TempDir(), "remap.json")
	started := time.Now()
	run.peak = peakHeapOver(func() {
		st, plan := stagePlanBoard(t, s, path)
		defer func() { require.NoError(t, st.Close()) }()
		run.counts = plan.counts
		plan.MarkApplied() // the report then lists the settled workflow choices
		require.NoError(t, plan.WriteReport(io.Discard))
		out, err := os.Create(remapFile)
		require.NoError(t, err)
		w := bufio.NewWriter(out)
		require.NoError(t, plan.WriteRemap(w))
		require.NoError(t, w.Flush())
		require.NoError(t, out.Close())
	})
	t.Logf("board %d MiB, %d incoming nodes, %d local: peak heap over start %d KiB (ceiling %d MiB), %s",
		run.size>>20, b.nodes, 3*b.each+1, run.peak>>10, stageHeapCeiling>>20, time.Since(started).Round(time.Second))
	return run
}

// planFixture returns a store and a staged board of a few dozen nodes that has
// been planned, with every kind of decision in its plan tables.
func planFixture(t *testing.T) (*Store, *StagedExport) {
	t.Helper()
	b := planBoard{nodes: 60, each: 6}
	s := planLocalStore(t, b.each)
	path := filepath.Join(t.TempDir(), "plan.json")
	writePlanBoard(t, path, b)
	st, _ := stagePlanBoard(t, s, path)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return s, st
}

// Two board sizes, a store that grows with each: both plan and report within
// the ceiling, every kind of decision is made, and the peak of the board eight
// times larger (on the file side and the local side) is not larger by more than
// planPeakGrowthLimit, so no per-node state is kept in memory.
func TestPlanImportStaged_PeakHeapDoesNotScaleWithBoard(t *testing.T) {
	if testing.Short() {
		t.Skip("generates a large board")
	}
	defer debug.SetGCPercent(debug.SetGCPercent(20))
	smallBoard := planBoard{nodes: planSmallNodes, each: planSmallEach}
	largeBoard := planBoard{nodes: planLargeNodes, each: planLargeEach}
	small := plannedPeakHeap(t, smallBoard)
	large := plannedPeakHeap(t, largeBoard)
	for b, run := range map[planBoard]planRun{smallBoard: small, largeBoard: large} {
		assert.Equal(t, b.provisional(), run.counts[secRemap], "provisional nodes renumbered")
		assert.Equal(t, b.each, run.counts[secLocalRenumber], "local tasks renumbered")
		assert.Equal(t, b.each, run.counts[secAdoption], "uids adopted")
		assert.Equal(t, b.each, run.counts[secWorkflow], "workflow conflicts settled")
	}
	require.Greater(t, large.size, int64(3*stageHeapCeiling), "the board must dwarf the ceiling")
	assert.Less(t, small.peak, uint64(stageHeapCeiling))
	assert.Less(t, large.peak, uint64(stageHeapCeiling), "planning + report output must not grow with the board")
	assert.Less(t, large.peak, small.peak+planPeakGrowthLimit, "the peak must not scale with the board")
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
