// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Tests for MTIX-95.31.13: a merge import never silently drops or reverts a
// workflow value (status, assignee, agent state, wake time). Written
// red-first against main 947b7c0, where mergeUnchangedContent dropped a
// teammate's claim and a teammate's text edit undid a local reopen.
// Parallel cases retain owned fixtures and existing behavior assertions.
package sqlite_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// workflowEdit changes one task's workflow fields in a store.
type workflowEdit func(t *testing.T, s *sqlite.Store)

func setStatus(status model.Status) workflowEdit {
	return func(t *testing.T, s *sqlite.Store) {
		t.Helper()
		require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{Status: &status}))
	}
}

func setAssignee(who string) workflowEdit {
	return func(t *testing.T, s *sqlite.Store) {
		t.Helper()
		require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{Assignee: &who}))
	}
}

func setAgentState(state model.AgentState) workflowEdit {
	return func(t *testing.T, s *sqlite.Store) {
		t.Helper()
		require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{AgentState: &state}))
	}
}

func deferUntil(at time.Time) workflowEdit {
	return func(t *testing.T, s *sqlite.Store) {
		t.Helper()
		require.NoError(t, s.DeferNode(context.Background(), "REC-1", &at, "later", "tester"))
	}
}

func editTitle(title string) workflowEdit {
	return func(t *testing.T, s *sqlite.Store) {
		t.Helper()
		require.NoError(t, s.UpdateNode(context.Background(), "REC-1", &store.NodeUpdate{Title: &title}))
	}
}

// workflowCase is one way the two sides' workflow values differ.
type workflowCase struct {
	name string
	// base is applied to both stores first.
	base workflowEdit
	// local and teammate are applied to the local store and the teammate's.
	local, teammate workflowEdit
	// teammateEditsText: the teammate also edits the title (content changes).
	teammateEditsText bool
	// field is the workflow field the conflict report names.
	field string
}

var wakeA = time.Date(2027, 1, 1, 9, 0, 0, 0, time.UTC)
var wakeB = time.Date(2027, 2, 1, 9, 0, 0, 0, time.UTC)

func noEdit(*testing.T, *sqlite.Store) {}

// workflowCases covers each field in both directions: a teammate's change
// the content-equal branch used to drop, and a local change a teammate's
// text edit used to undo.
func workflowCases() []workflowCase {
	return []workflowCase{
		{name: "teammate claim, content unchanged", base: noEdit, teammate: setStatus(model.StatusInProgress), field: "status"},
		{name: "teammate assigns, content unchanged", base: noEdit, teammate: setAssignee("bob"), field: "assignee"},
		{name: "teammate agent state, content unchanged", base: noEdit, teammate: setAgentState(model.AgentStateStuck), field: "agent_state"},
		{name: "teammate wake time, content unchanged", base: deferUntil(wakeA), teammate: deferUntil(wakeB), field: "defer_until"},
		{name: "local reopen undone by text edit", base: setStatus(model.StatusDone), local: setStatus(model.StatusOpen),
			teammateEditsText: true, field: "status"},
		{name: "local reassign undone by text edit", base: setAssignee("alice"), local: setAssignee("carol"),
			teammateEditsText: true, field: "assignee"},
		{name: "local agent state undone by text edit", base: noEdit, local: setAgentState(model.AgentStateWorking),
			teammateEditsText: true, field: "agent_state"},
		{name: "local wake time undone by text edit", base: deferUntil(wakeA), local: deferUntil(wakeB),
			teammateEditsText: true, field: "defer_until"},
	}
}

// workflowStores returns the local store and the teammate's export of the
// shared task REC-1 after the case's edits.
func workflowStores(t *testing.T, tc workflowCase) (*sqlite.Store, *sqlite.Store, *sqlite.ExportData) {
	t.Helper()
	local, teammate := newTestStore(t), newTestStore(t)
	shared := taskUID(t)
	createSameIDTask(t, local, "REC-1", "", 1, shared, "Shared task")
	createSameIDTask(t, teammate, "REC-1", "", 1, shared, "Shared task")
	for _, s := range []*sqlite.Store{local, teammate} {
		tc.base(t, s)
	}
	if tc.local != nil {
		tc.local(t, local)
	}
	if tc.teammate != nil {
		tc.teammate(t, teammate)
	}
	if tc.teammateEditsText {
		editTitle("Shared task, reworded")(t, teammate)
	}
	data, err := teammate.Export(context.Background(), "", "")
	require.NoError(t, err)
	return local, teammate, data
}

// TestImportReconcile_WorkflowDiffers_RefusesAndWritesNothing verifies the
// merge refuses, writing nothing, when any workflow field differs, whichever
// side changed it (MTIX-95.31.13).
func TestImportReconcile_WorkflowDiffers_RefusesAndWritesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range workflowCases() {
		t.Run(tc.name, func(t *testing.T) {
			local, _, file := workflowStores(t, tc)
			before := storeSnapshotJSON(t, local)

			_, result, err := local.ImportReconcile(context.Background(), file,
				sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})

			require.Error(t, err, "a merge over differing workflow values must be refused")
			assert.Nil(t, result)
			assert.Equal(t, before, storeSnapshotJSON(t, local), "a refusal writes nothing")
		})
	}
}

// TestImportReconcile_ProgressOnlyDifference_MergesSilently verifies a
// difference in progress alone (recalculated, never a conflict) is merged.
func TestImportReconcile_ProgressOnlyDifference_MergesSilently(t *testing.T) {
	t.Parallel()
	local, _, file := workflowStores(t, workflowCase{name: "none", base: noEdit})
	file.Nodes[0].Progress = 0.5
	file.Checksum = sqlite.RecomputeChecksumForTest(t, file)

	_, _, err := local.ImportReconcile(context.Background(), file,
		sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.NoError(t, err)
}
