// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-2.3.2.2 (FR-7.8): the plan made from the staged export (indexed queries
// against the staged rows and the local records) is the plan ImportReconcile
// makes in memory: the same report, the same remap, the same refusals, for
// every kind of decision, and it writes nothing to the store.
package sqlite_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// stageData stages the export document of data.
func stageData(t *testing.T, data *sqlite.ExportData) *sqlite.StagedExport {
	t.Helper()
	raw, err := json.Marshal(data)
	require.NoError(t, err)
	st, err := sqlite.StageExport(context.Background(), bytes.NewReader(raw), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, st.Close()) })
	return st
}

// outputsOf renders the three outputs of a report source.
func outputsOf(t *testing.T, src sqlite.ReportSource) (text, remap, doc string, hasRemap bool) {
	t.Helper()
	var a, b, c bytes.Buffer
	require.NoError(t, src.WriteReport(&a))
	has, err := src.HasRemap()
	require.NoError(t, err)
	if has {
		require.NoError(t, src.WriteRemap(&b))
	}
	require.NoError(t, src.WriteWorkflowJSON(&c))
	return a.String(), b.String(), c.String(), has
}

var uuidText = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)

// planMatchesReconcile plans the import from the staged file, checks the
// store is untouched, runs ImportReconcile on the same store and file, and
// requires the plan's errors and outputs to equal the report's. mask hides
// the uids an import mints (a force-rename's), which differ by design.
func planMatchesReconcile(t *testing.T, local *sqlite.Store, file func() *sqlite.ExportData,
	opts sqlite.ImportReconcileOptions, mask bool) (*sqlite.ImportReconcileReport, error) {
	t.Helper()
	ctx := context.Background()
	before := storeSnapshotJSON(t, local)
	plan, planErr := local.PlanImportStaged(ctx, stageData(t, file()), opts)
	require.NotNil(t, plan)
	require.Equal(t, before, storeSnapshotJSON(t, local), "planning writes nothing")

	report, _, err := local.ImportReconcile(ctx, file(), opts)
	require.NotNil(t, report)
	if report.Applied {
		plan.MarkApplied()
	}
	if err != nil {
		require.Error(t, planErr)
		assert.Equal(t, err.Error(), planErr.Error())
	} else {
		require.NoError(t, planErr)
	}
	wantText, wantRemap, wantDoc, wantHas := outputsOf(t, report)
	gotText, gotRemap, gotDoc, gotHas := outputsOf(t, plan)
	if mask {
		for _, s := range []*string{&wantRemap, &gotRemap} {
			*s = uuidText.ReplaceAllString(*s, "UID")
		}
		// In the report a minted uid is numbered by its first appearance, so a
		// minted uid read where another uid belongs still shows.
		for _, s := range []*string{&wantText, &gotText} {
			seen := map[string]int{}
			*s = uuidText.ReplaceAllStringFunc(*s, func(u string) string {
				if _, ok := seen[u]; !ok {
					seen[u] = len(seen) + 1
				}
				return fmt.Sprintf("UID%d", seen[u])
			})
		}
	}
	assert.Equal(t, wantText, gotText)
	assert.Equal(t, wantHas, gotHas)
	if !mask {
		assert.Equal(t, wantRemap, gotRemap)
	}
	assert.Equal(t, wantDoc, gotDoc)
	return report, err
}

// A merge that renumbers local tasks (the file holds different tasks under
// their ids), refused without confirmation and applied with it.
func TestPlanImportStaged_LocalRenumbers(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		local, file, _, _ := sameIDStores(t)
		report, _ := planMatchesReconcile(t, local, file,
			sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: confirm}, false)
		assert.Len(t, report.LocalRenumbers, 2)
		assert.Equal(t, confirm, report.Applied)
	}
}

// Every kind of uid adoption, move and title decision of a merge.
func TestPlanImportStaged_MergeDecisions(t *testing.T) {
	type build func(t *testing.T, local, teammate *sqlite.Store)
	cases := map[string]build{
		"adopted backfill uids": func(t *testing.T, l, m *sqlite.Store) {
			createSameIDTask(t, l, "REC-1", "", 1, backfilledUID(t), "A pre-upgrade task")
			createSameIDTask(t, m, "REC-1", "", 1, backfilledUID(t), "B pre-upgrade task")
		},
		"local task without a uid": func(t *testing.T, l, m *sqlite.Store) {
			createSameIDTask(t, l, "REC-1", "", 1, taskUID(t), "Shared task")
			clearUIDs(t, l)
			createSameIDTask(t, m, "REC-1", "", 1, taskUID(t), "Shared task")
		},
		"swapped ids": func(t *testing.T, l, m *sqlite.Store) {
			a, b := backfilledUID(t), backfilledUID(t)
			createSameIDTask(t, l, "REC-1", "", 1, a, "Task A")
			createSameIDTask(t, l, "REC-2", "", 2, b, "Task B")
			createSameIDTask(t, m, "REC-1", "", 1, b, "Task B")
			createSameIDTask(t, m, "REC-2", "", 2, a, "Task A")
		},
		"moved to the id the file holds it under": func(t *testing.T, l, m *sqlite.Store) {
			uid := taskUID(t)
			createSameIDTask(t, l, "REC-1", "", 1, uid, "Moved task")
			createSameIDTask(t, l, "REC-1.1", "REC-1", 1, taskUID(t), "Its child")
			createSameIDTask(t, m, "REC-4", "", 4, uid, "Moved task")
		},
		"no uid to compare, another title": func(t *testing.T, l, m *sqlite.Store) {
			createSameIDTask(t, l, "REC-1", "", 1, taskUID(t), "Local task")
			createSameIDTask(t, l, "REC-1.1", "REC-1", 1, taskUID(t), "Its subtask")
			createSameIDTask(t, m, "REC-1", "", 1, backfilledUID(t), "Teammate task")
			clearUIDs(t, l)
			clearUIDs(t, m)
		},
		"uid at another parent": func(t *testing.T, l, m *sqlite.Store) {
			uid := taskUID(t)
			createSameIDTask(t, l, "REC-1", "", 1, taskUID(t), "Parent one")
			createSameIDTask(t, l, "REC-2", "", 2, taskUID(t), "Parent two")
			createSameIDTask(t, l, "REC-1.1", "REC-1", 1, uid, "Child")
			createSameIDTask(t, m, "REC-1", "", 1, taskUID(t), "Parent one")
			createSameIDTask(t, m, "REC-2", "", 2, taskUID(t), "Parent two")
			createSameIDTask(t, m, "REC-2.1", "REC-2", 1, uid, "Child")
		},
		"a soft-deleted local task": func(t *testing.T, l, m *sqlite.Store) {
			createSameIDTask(t, l, "REC-1", "", 1, backfilledUID(t), "Shared task")
			require.NoError(t, l.DeleteNode(context.Background(), "REC-1", false, "agent-local"))
			createSameIDTask(t, m, "REC-1", "", 1, backfilledUID(t), "Shared task")
		},
	}
	wants := map[string]string{
		"adopted backfill uids": "uids adopted from the file", "local task without a uid": "local uid=(new)",
		"swapped ids": "local tasks moved to the id", "moved to the id the file holds it under": "local tasks moved to the id",
		"no uid to compare, another title": "a different title and no uid to compare",
		"uid at another parent":            "REJECTED uid conflicts", "a soft-deleted local task": "uids adopted from the file",
	}
	for name, setup := range cases {
		for _, confirm := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/confirm=%t", name, confirm), func(t *testing.T) {
				local, teammate := newTestStore(t), newTestStore(t)
				setup(t, local, teammate)
				// A task without a uid is given a minted one, which differs by design.
				report, _ := planMatchesReconcile(t, local, func() *sqlite.ExportData { return exportOf(t, teammate) },
					sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: confirm}, name == "no uid to compare, another title")
				assert.Contains(t, report.String(), wants[name], "the case must exercise what it names")
			})
		}
	}
}

// The rejected collisions, in a replace and for a uid repeated in the file, and
// a force-rename that re-stamps instead.
func TestPlanImportStaged_UIDCollisions(t *testing.T) {
	build := func(t *testing.T) (*sqlite.Store, func() *sqlite.ExportData) {
		local, teammate := newTestStore(t), newTestStore(t)
		uid := taskUID(t)
		createSameIDTask(t, local, "REC-1", "", 1, uid, "Local")
		createSameIDTask(t, teammate, "REC-2", "", 2, uid, "Other number")
		createSameIDTask(t, teammate, "REC-3", "", 3, taskUID(t), "Dup one")
		return local, func() *sqlite.ExportData {
			data := exportOf(t, teammate)
			data.Nodes[len(data.Nodes)-1].UID = data.Nodes[0].UID // a uid shared inside the file
			return data
		}
	}
	for name, opts := range map[string]sqlite.ImportReconcileOptions{
		"replace":            {Mode: sqlite.ImportModeReplace},
		"merge":              {Mode: sqlite.ImportModeMerge},
		"replace with force": {Mode: sqlite.ImportModeReplace, ForceRename: true},
	} {
		t.Run(name, func(t *testing.T) {
			local, file := build(t)
			_, err := planMatchesReconcile(t, local, file, opts, opts.ForceRename)
			require.ErrorIs(t, err, model.ErrConflict)
		})
	}
	t.Run("an idempotent no-op", func(t *testing.T) {
		local, teammate := newTestStore(t), newTestStore(t)
		uid := taskUID(t)
		createSameIDTask(t, local, "REC-1", "", 1, uid, "Same")
		createSameIDTask(t, teammate, "REC-1", "", 1, uid, "Same")
		report, err := planMatchesReconcile(t, local, func() *sqlite.ExportData { return exportOf(t, teammate) },
			sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge}, false)
		require.NoError(t, err)
		assert.Equal(t, 1, report.Idempotent)
	})
}

// Provisional nodes renumber under a rebased parent, shallowest first, taking
// the numbers the store and the plan have free; a live store needs confirmation.
func TestPlanImportStaged_ProvisionalSubtreeRemaps(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	build := func(t *testing.T) (*sqlite.Store, func() *sqlite.ExportData) {
		s := newTestStore(t)
		ctx := context.Background()
		for _, n := range []model.Node{
			{ID: "REC-1", Project: "REC", Seq: 1, Title: "Root", NodeType: model.NodeTypeEpic},
			{ID: "REC-1.1", ParentID: "REC-1", Project: "REC", Depth: 1, Seq: 1, Title: "Existing", NodeType: model.NodeTypeStory},
		} {
			n.Status, n.Priority, n.Weight, n.ContentHash = model.StatusOpen, model.PriorityMedium, 1.0, "h"+n.ID
			n.UID, n.CreatedAt, n.UpdatedAt = newUID(t), now, now
			require.NoError(t, s.CreateNode(ctx, &n))
		}
		pu, cu, gu, su := newUID(t), newUID(t), newUID(t), newUID(t)
		parent, sibling := provisionalPath(t, "REC-1", pu), provisionalPath(t, "REC-1", su)
		node := func(id, par string, depth int, uid, title string) sqlite.TestExportNode {
			return sqlite.TestExportNode{ID: id, ParentID: par, Project: "REC", Depth: depth, Seq: 1, Title: title,
				ContentHash: "x" + title, UID: uid, CreatedAt: now, UpdatedAt: now}
		}
		return s, func() *sqlite.ExportData {
			return reconcileExport(t, "REC", node(parent, "REC-1", 1, pu, "P"), node(sibling, "REC-1", 1, su, "S"),
				node(parent+".1", parent, 2, cu, "C"), node(parent+".1.1", parent+".1", 3, gu, "G"))
		}
	}
	for _, confirm := range []bool{false, true} {
		local, file := build(t)
		report, _ := planMatchesReconcile(t, local, file,
			sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Confirm: confirm}, false)
		assert.Len(t, report.Remaps, 4)
	}
}

// Workflow values that differ: refused without a choice, applied with a side,
// with the history of each side listed, and a named task that does not conflict.
func TestPlanImportStaged_WorkflowConflicts(t *testing.T) {
	for _, tc := range workflowCases() {
		for name, res := range map[string]sqlite.WorkflowResolution{
			"unresolved": {}, "theirs": {Prefer: sqlite.WorkflowTheirs}, "ours": {Prefer: sqlite.WorkflowOurs},
		} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				local, _, file := workflowStores(t, tc)
				_, err := planMatchesReconcile(t, local, func() *sqlite.ExportData { return file },
					sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Workflow: res}, false)
				assert.Equal(t, name == "unresolved", err != nil)
			})
		}
	}
	t.Run("history of both sides", func(t *testing.T) {
		f, file, _ := workflowHistoryPair(t, 7)
		_, err := planMatchesReconcile(t, f.s, func() *sqlite.ExportData { return file },
			sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge}, false)
		require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	})
	t.Run("a named task that does not conflict", func(t *testing.T) {
		local, _, file := workflowStores(t, workflowCases()[0])
		res := sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs, Ours: []string{"REC-99"}}
		_, err := planMatchesReconcile(t, local, func() *sqlite.ExportData { return file },
			sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Workflow: res}, false)
		require.ErrorIs(t, err, model.ErrInvalidInput)
	})
}

// A local title and assignee with invalid UTF-8 print in the staged report as
// the in-memory report prints them: quoted from the stored bytes (MTIX-2.3.2.2).
func TestPlanImportStaged_InvalidUTF8WorkflowText_ReportEqualsInMemory(t *testing.T) {
	for name, res := range map[string]sqlite.WorkflowResolution{
		"unresolved": {}, "theirs": {Prefer: sqlite.WorkflowTheirs}} {
		t.Run(name, func(t *testing.T) {
			local, _, file := workflowStores(t, workflowCases()[0])
			_, err := local.WriteDB().ExecContext(context.Background(),
				`UPDATE nodes SET title = ?, assignee = ? WHERE id = 'REC-1'`, "Shared \xff task", "ali\xc3ce")
			require.NoError(t, err)
			report, _ := planMatchesReconcile(t, local, func() *sqlite.ExportData { return file },
				sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Workflow: res}, false)
			assert.Contains(t, report.String(), `\xff`, "the case must carry the invalid bytes into the report")
		})
	}
}

// A file that repeats a task id: --theirs and --ours by id settle the last
// conflict of the id only, as the in-memory plan (which keys them by id) does.
func TestPlanImportStaged_RepeatedTaskID_ChoiceByIDEqualsInMemory(t *testing.T) {
	for name, res := range map[string]sqlite.WorkflowResolution{
		"theirs by id":            {Theirs: []string{"REC-1"}},
		"ours by id":              {Ours: []string{"REC-1"}},
		"a task with no conflict": {Prefer: sqlite.WorkflowOurs, Theirs: []string{"REC-99"}},
	} {
		t.Run(name, func(t *testing.T) {
			local, _, file := workflowStores(t, workflowCases()[0])
			dup := file.Nodes[0]
			dup.UID, dup.Status = "", "blocked"
			file.Nodes = append(file.Nodes, dup)
			require.NoError(t, sqlite.RecomputeExportChecksum(file))
			_, err := planMatchesReconcile(t, local, func() *sqlite.ExportData { return file },
				sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Workflow: res}, false)
			if res.Prefer == "" {
				require.Error(t, err, "the earlier conflict of the repeated id is left without a choice")
			}
		})
	}
}

// A force-rename that goes past the rejection: the fresh uid flows into the
// provisional remap, the uid adoptions and the identity decisions, in a merge
// and in a replace, as in the in-memory plan.
func TestPlanImportStaged_ForceRename_FreshUIDReachesRemapsAndAdoptions(t *testing.T) {
	build := func(t *testing.T) (*sqlite.Store, func() *sqlite.ExportData) {
		local := newTestStore(t)
		uidA, uidV, uidX, uidY := taskUID(t), taskUID(t), taskUID(t), taskUID(t)
		for i, uid := range []string{uidA, uidV, uidX, uidY} {
			createSameIDTask(t, local, fmt.Sprintf("REC-%d", i+1), "", i+1, uid, fmt.Sprintf("Local %d", i+1))
		}
		node := func(id, parent string, depth, seq int, uid string) sqlite.TestExportNode {
			return sqlite.TestExportNode{ID: id, ParentID: parent, Depth: depth, Seq: seq, Project: "REC", Title: id,
				ContentHash: "h" + id, UID: uid, CreatedAt: sameIDTime, UpdatedAt: sameIDTime}
		}
		return local, func() *sqlite.ExportData {
			return reconcileExport(t, "REC", node("REC-1", "", 0, 1, uidA), node("REC-2", "", 0, 2, backfilledUID(t)),
				node("REC-3", "", 0, 3, uidV), node(provisionalPath(t, "REC-1", uidY), "REC-1", 1, 1, uidY))
		}
	}
	for name, mode := range map[string]sqlite.ImportMode{"merge": sqlite.ImportModeMerge, "replace": sqlite.ImportModeReplace} {
		t.Run(name, func(t *testing.T) {
			local, file := build(t)
			report, err := planMatchesReconcile(t, local, file,
				sqlite.ImportReconcileOptions{Mode: mode, ForceRename: true, Confirm: true}, true)
			require.NoError(t, err)
			assert.Len(t, report.Renamed, 2)
			assert.Len(t, report.Remaps, 1, "the provisional node is renumbered under its fresh uid")
			if mode == sqlite.ImportModeMerge {
				assert.Len(t, report.UIDAdoptions, 2, "the re-stamped and the backfilled uid are adopted")
			}
		})
	}
}

// A nil staged export is an invalid input, not a panic.
func TestPlanImportStaged_NilStagedExport(t *testing.T) {
	_, err := newTestStore(t).PlanImportStaged(context.Background(), nil, sqlite.ImportReconcileOptions{})
	require.ErrorIs(t, err, model.ErrInvalidInput)
}
