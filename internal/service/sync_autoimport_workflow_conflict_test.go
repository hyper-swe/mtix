// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestAutoImport_RefusalRecommendingMerge_ExplainsTheWorkflowConflictFlow
// verifies the refusal the automatic import prints (it runs replace mode
// and refuses a lossy file) explains that the merge it recommends refuses a
// task whose status, assignee, agent state or wake time differ until the
// user chooses --prefer theirs|ours, and that the merge then behaves so
// (MTIX-95.31.13): the teammate's claim is neither dropped nor applied
// without a choice.
func TestAutoImport_RefusalRecommendingMerge_ExplainsTheWorkflowConflictFlow(t *testing.T) {
	ctx := context.Background()
	pair := clones(t, newGuardFixture(t), 2)
	a, b := pair[0], pair[1]
	desc := "Local description"
	require.NoError(t, a.store.UpdateNode(ctx, "PROJ-2", &store.NodeUpdate{Description: &desc}))
	require.NoError(t, a.svc.AutoExport(ctx, a.mtixDir))
	require.NoError(t, b.store.ClaimNode(ctx, "PROJ-2", "bob"))
	require.NoError(t, b.svc.AutoExport(ctx, b.mtixDir))
	a.pull(t, asOlderClientBoard(t, b.read(t, "tasks.json"))) // a 0.5.3 board: a copy that is not current

	require.ErrorIs(t, a.svc.AutoImport(ctx, a.mtixDir), service.ErrAutoImportRefused)
	msg := a.notices.String()
	assert.Contains(t, msg, "never guesses")
	assert.Contains(t, msg, "--prefer theirs|ours")
	assert.Contains(t, msg, "--theirs ID,ID and --ours ID,ID")

	report, _, err := a.store.ImportReconcile(ctx, a.pulledBoard(t), sqlite.ImportReconcileOptions{Mode: sqlite.ImportModeMerge})
	require.ErrorIs(t, err, sqlite.ErrWorkflowConflict)
	assert.Equal(t, "PROJ-2", report.WorkflowConflicts[0].ID)
	node, err := a.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, model.StatusOpen, node.Status, "nothing was written")

	_, _, err = a.store.ImportReconcile(ctx, a.pulledBoard(t), sqlite.ImportReconcileOptions{
		Mode: sqlite.ImportModeMerge, Workflow: sqlite.WorkflowResolution{Prefer: sqlite.WorkflowTheirs},
	})
	require.NoError(t, err)
	node, err = a.store.GetNode(ctx, "PROJ-2")
	require.NoError(t, err)
	assert.Equal(t, "bob", node.Assignee, "the teammate's claim is applied by the choice")
	assert.Equal(t, desc, node.Description, "the listed field value is kept")
}
