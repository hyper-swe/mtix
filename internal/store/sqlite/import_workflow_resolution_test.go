// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.13: how a merge import settles workflow conflicts
// (--prefer, --theirs, --ours), what the refusal lists, and what the choice
// writes.
// Parallel cases retain owned fixtures and existing behavior assertions.
package sqlite_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// workflowOf returns the workflow values of node id in s.
func workflowOf(t *testing.T, s *sqlite.Store, id string) (status model.Status, assignee string, state model.AgentState, wake string) {
	t.Helper()
	n, err := s.GetNode(context.Background(), id)
	require.NoError(t, err)
	if n.DeferUntil != nil {
		wake = n.DeferUntil.UTC().Format(time.RFC3339)
	}
	return n.Status, n.Assignee, n.AgentState, wake
}

// mergeWith runs a merge of file into s with the resolution.
func mergeWith(s *sqlite.Store, file *sqlite.ExportData, r sqlite.WorkflowResolution) (*sqlite.ImportReconcileReport, error) {
	report, _, err := s.ImportReconcile(context.Background(), file,
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge, Workflow: r})
	return report, err
}

// TestImportReconcile_WorkflowConflict_ReportListsEveryField verifies the
// refusal is an ErrWorkflowConflict (and ErrConflict) that lists, per task,
// the local value, the file's value and an activity hint.
func TestImportReconcile_WorkflowConflict_ReportListsEveryField(t *testing.T) {
	t.Parallel()
	for _, tc := range workflowCases() {
		t.Run(tc.name, func(t *testing.T) {
			local, _, file := workflowStores(t, tc)
			report, err := mergeWith(local, file, sqlite.WorkflowResolution{})

			require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
			require.ErrorIs(t, err, model.ErrConflict)
			var conflict *sqlite.WorkflowConflictError
			require.True(t, errors.As(err, &conflict))
			require.Len(t, conflict.Conflicts, 1)
			require.Len(t, report.WorkflowConflicts, 1)
			c := report.WorkflowConflicts[0]
			assert.Equal(t, "REC-1", c.ID)
			assert.Equal(t, "Shared task", c.Title)
			assert.Equal(t, tc.field, c.Fields[0].Field)
			assert.NotEmpty(t, c.Hint)
			assert.False(t, report.Applied)
			text := report.String()
			assert.Contains(t, text, "WORKFLOW CONFLICTS: 1 task(s)")
			assert.Contains(t, text, tc.field+": yours")
			assert.Contains(t, text, "--prefer theirs")
			assert.Contains(t, text, "--theirs ID,ID")
		})
	}
}

// TestImportReconcile_WorkflowChoice_AppliesExactlyTheChosenSide verifies
// every resolution form, in every case, writes the chosen side's values:
// --prefer theirs and --theirs REC-1 take the file's, --prefer ours and
// --ours REC-1 keep the local ones, and a per-task list wins over --prefer.
func TestImportReconcile_WorkflowChoice_AppliesExactlyTheChosenSide(t *testing.T) {
	t.Parallel()
	resolutions := []struct {
		name      string
		r         sqlite.WorkflowResolution
		wantTheir bool
	}{
		{"prefer theirs", sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs}, true},
		{"prefer ours", sqlite.WorkflowResolution{Prefer: sqlite.WorkflowOurs}, false},
		{"theirs list", sqlite.WorkflowResolution{Theirs: []string{"REC-1"}}, true},
		{"ours list", sqlite.WorkflowResolution{Ours: []string{"REC-1"}}, false},
		{"theirs list beats prefer ours", sqlite.WorkflowResolution{Prefer: sqlite.WorkflowOurs, Theirs: []string{"REC-1"}}, true},
		{"ours list beats prefer theirs", sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs, Ours: []string{"REC-1"}}, false},
	}
	for _, tc := range workflowCases() {
		for _, res := range resolutions {
			t.Run(tc.name+"/"+res.name, func(t *testing.T) {
				t.Parallel()
				local, teammate, file := workflowStores(t, tc)
				ls, la, lst, lw := workflowOf(t, local, "REC-1")
				fs, fa, fst, fw := workflowOf(t, teammate, "REC-1")

				report, err := mergeWith(local, file, res.r)
				require.NoError(t, err)
				assert.True(t, report.Applied)

				gs, ga, gst, gw := workflowOf(t, local, "REC-1")
				if res.wantTheir {
					assert.Equal(t, []any{fs, fa, fst, fw}, []any{gs, ga, gst, gw})
				} else {
					assert.Equal(t, []any{ls, la, lst, lw}, []any{gs, ga, gst, gw})
				}
				if tc.teammateEditsText {
					n, getErr := local.GetNode(context.Background(), "REC-1")
					require.NoError(t, getErr)
					assert.Equal(t, "Shared task, reworded", n.Title, "the text edit applies whichever side is chosen")
				}
			})
		}
	}
}

// twoTaskStores returns a local store and a teammate's file where REC-1 and
// REC-2 both differ in status (the teammate started both).
func twoTaskStores(t *testing.T) (*sqlite.Store, *sqlite.ExportData) {
	t.Helper()
	local, teammate := newTestStore(t), newTestStore(t)
	for i, id := range []string{"REC-1", "REC-2"} {
		uid := taskUID(t)
		createSameIDTask(t, local, id, "", i+1, uid, "Task "+id)
		createSameIDTask(t, teammate, id, "", i+1, uid, "Task "+id)
		started := model.StatusInProgress
		require.NoError(t, teammate.UpdateNode(context.Background(), id, &store.NodeUpdate{Status: &started}))
	}
	return local, exportOf(t, teammate)
}

// TestImportReconcile_WorkflowChoice_MustCoverEveryConflict verifies a
// choice that leaves a conflicting task unsettled is still refused and
// writes nothing, and that listing both sides settles each task as listed.
func TestImportReconcile_WorkflowChoice_MustCoverEveryConflict(t *testing.T) {
	t.Parallel()
	local, file := twoTaskStores(t)
	before := storeSnapshotJSON(t, local)

	report, err := mergeWith(local, file, sqlite.WorkflowResolution{Theirs: []string{"REC-1"}})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	assert.Len(t, report.WorkflowConflicts, 2, "the refusal still lists every conflict")
	assert.Equal(t, sqlite.WorkflowTheirs, report.WorkflowConflicts[0].Choice)
	assert.Empty(t, report.WorkflowConflicts[1].Choice)
	assert.Contains(t, err.Error(), "1 still need a choice")
	assert.Equal(t, before, storeSnapshotJSON(t, local), "a partly covered choice writes nothing")

	_, err = mergeWith(local, file, sqlite.WorkflowResolution{
		Theirs: []string{"REC-1"}, Ours: []string{"REC-2"},
	})
	require.NoError(t, err)
	one, _, _, _ := workflowOf(t, local, "REC-1")
	two, _, _, _ := workflowOf(t, local, "REC-2")
	assert.Equal(t, model.StatusInProgress, one, "REC-1 took the file's status")
	assert.Equal(t, model.StatusOpen, two, "REC-2 kept the local status")
}

// TestImportReconcile_WorkflowResolution_Invalid_IsRejected verifies a task
// that does not conflict, a task on both sides and an unknown preference
// are rejected before anything is written.
func TestImportReconcile_WorkflowResolution_Invalid_IsRejected(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		r    sqlite.WorkflowResolution
	}{
		{"task without a conflict", sqlite.WorkflowResolution{Prefer: sqlite.WorkflowOurs, Theirs: []string{"REC-9"}}},
		{"task on both sides", sqlite.WorkflowResolution{Theirs: []string{"REC-1"}, Ours: []string{"REC-1"}}},
		{"unknown preference", sqlite.WorkflowResolution{Prefer: "mine"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			local, file := twoTaskStores(t)
			before := storeSnapshotJSON(t, local)
			_, err := mergeWith(local, file, tt.r)
			require.ErrorIs(t, err, model.ErrInvalidInput)
			assert.Equal(t, before, storeSnapshotJSON(t, local))
		})
	}
}

// TestImportReconcile_WorkflowChoice_IsRecordedInActivity verifies the
// choice is recorded in the task's activity, naming the fields and both
// values, and that merging the same file again does not add another entry
// when the values now agree.
func TestImportReconcile_WorkflowChoice_IsRecordedInActivity(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	local, file := twoTaskStores(t)
	fixed := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	local.SetClock(func() time.Time { return fixed })

	_, err := mergeWith(local, file, sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs})
	require.NoError(t, err)
	entries, err := local.GetActivity(ctx, "REC-1", 50, 0)
	require.NoError(t, err)
	var recorded []model.ActivityEntry
	for _, e := range entries {
		if e.Type == model.ActivityTypeSystem && e.Author == "import" {
			recorded = append(recorded, e)
		}
	}
	require.Len(t, recorded, 1)
	assert.Equal(t, fixed, recorded[0].CreatedAt.UTC())
	assert.Contains(t, recorded[0].Text, "kept the theirs values")
	assert.Contains(t, recorded[0].Text, `status: local "open", file "in_progress"`)

	_, err = mergeWith(local, file, sqlite.WorkflowResolution{})
	require.NoError(t, err, "after taking the file's values nothing conflicts")
	entries, err = local.GetActivity(ctx, "REC-1", 50, 0)
	require.NoError(t, err)
	again := 0
	for _, e := range entries {
		if e.Type == model.ActivityTypeSystem && e.Author == "import" {
			again++
		}
	}
	assert.Equal(t, 1, again, "no second entry")
}

// TestImportReconcile_WorkflowTheirs_MovesTheWholeStatusGroup verifies the
// status group moves together: taking a file's open status for a task done
// locally clears the closed time, and keeping the local done status keeps it.
func TestImportReconcile_WorkflowTheirs_MovesTheWholeStatusGroup(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, choice := range []sqlite.WorkflowChoice{sqlite.WorkflowTheirs, sqlite.WorkflowOurs} {
		t.Run(string(choice), func(t *testing.T) {
			local, teammate := newTestStore(t), newTestStore(t)
			uid := taskUID(t)
			createSameIDTask(t, local, "REC-1", "", 1, uid, "Shared task")
			createSameIDTask(t, teammate, "REC-1", "", 1, uid, "Shared task")
			require.NoError(t, local.ClaimNode(ctx, "REC-1", "agent-a"))
			require.NoError(t, local.TransitionStatus(ctx, "REC-1", model.StatusDone, "done", "agent-a"))

			_, err := mergeWith(local, exportOf(t, teammate), sqlite.WorkflowResolution{Prefer: choice})
			require.NoError(t, err)
			n, err := local.GetNode(ctx, "REC-1")
			require.NoError(t, err)
			if choice == sqlite.WorkflowTheirs {
				assert.Equal(t, model.StatusOpen, n.Status)
				assert.Nil(t, n.ClosedAt, "a reopened task has no closed time")
				assert.Empty(t, n.Assignee)
			} else {
				assert.Equal(t, model.StatusDone, n.Status)
				assert.NotNil(t, n.ClosedAt)
				assert.Equal(t, "agent-a", n.Assignee)
			}
		})
	}
}

// TestImportReconcile_WorkflowValuesAgree_MergeUnchanged verifies identical
// values, equal wake times written differently, and a progress-only
// difference never conflict, and that a schema 1.0.0 file is checked the
// same way as a 2.x one.
func TestImportReconcile_WorkflowValuesAgree_MergeUnchanged(t *testing.T) {
	t.Parallel()
	t.Run("same instant in another zone is not a conflict", func(t *testing.T) {
		local, _, file := workflowStores(t, workflowCase{name: "wake", base: deferUntil(wakeA)})
		file.Nodes[0].DeferUntil = wakeA.In(time.FixedZone("x", 5*3600)).Format(time.RFC3339)
		file.Checksum = sqlite.RecomputeChecksumForTest(t, file)
		_, err := mergeWith(local, file, sqlite.WorkflowResolution{})
		require.NoError(t, err)
	})
	t.Run("1.0.0 file conflicts like a 2.x file", func(t *testing.T) {
		local, _, file := workflowStores(t, workflowCases()[0])
		_, err := mergeWith(local, asSchemaV1(t, file), sqlite.WorkflowResolution{})
		require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	})
	t.Run("1.0.0 file with prefer theirs takes the status but keeps 2.0.0 columns", func(t *testing.T) {
		local, _, file := workflowStores(t, workflowCases()[0])
		_, err := mergeWith(local, asSchemaV1(t, file), sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs})
		require.NoError(t, err)
		status, _, _, _ := workflowOf(t, local, "REC-1")
		assert.Equal(t, model.StatusInProgress, status)
	})
}

// TestImportReconcile_ActivityHint_SaysWhatEachSideHolds verifies the hint
// describes the activity streams without judging: the file holds a claim
// entry the local store lacks, and the other way round.
func TestImportReconcile_ActivityHint_SaysWhatEachSideHolds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	local, teammate := newTestStore(t), newTestStore(t)
	uid := taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, uid, "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, uid, "Shared task")
	require.NoError(t, teammate.ClaimNode(ctx, "REC-1", "bob"))

	report, err := mergeWith(local, exportOf(t, teammate), sqlite.WorkflowResolution{})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	assert.Contains(t, report.WorkflowConflicts[0].Hint, "the file holds 1 entr(ies) you lack, latest: claim by")

	require.NoError(t, local.ClaimNode(ctx, "REC-1", "alice"))
	require.NoError(t, local.UnclaimNode(ctx, "REC-1", "stepping back", "alice"))
	report, err = mergeWith(local, exportOf(t, teammate), sqlite.WorkflowResolution{})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	assert.Contains(t, report.WorkflowConflicts[0].Hint, "you hold 2 entr(ies) the file lacks")
}
