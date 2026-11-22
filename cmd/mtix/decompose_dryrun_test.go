// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestRunDecomposeDryRun previews child creation without writing them — the
// 'mtix decompose --dry-run' path (runDecomposeDryRun + printDryRunResult).
func TestRunDecomposeDryRun(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	parent, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "epic", Creator: "u"})
	require.NoError(t, err)

	out := captureStdout(t, func() {
		require.NoError(t, runDecomposeDryRun(parent.ID, []service.DecomposeInput{
			{Title: "child one"}, {Title: "child two"},
		}))
	})
	assert.Contains(t, out, "child one")
	assert.Contains(t, out, "child two")

	// No children were actually created (dry run).
	kids, err := app.store.GetDirectChildren(ctx, parent.ID)
	require.NoError(t, err)
	assert.Empty(t, kids, "dry-run must not persist children")
}

// TestRunDecomposeDryRun_IssueType_PrintedInJSON covers MTIX-279 on the CLI dry run.
func TestRunDecomposeDryRun_IssueType_PrintedInJSON(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	parent, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "epic", Creator: "u"})
	require.NoError(t, err)
	app.jsonOutput = true
	t.Cleanup(func() { app.jsonOutput = false })

	out := captureStdout(t, func() {
		require.NoError(t, runDecomposeDryRun(parent.ID, []service.DecomposeInput{{Title: "c", IssueType: "bug"}}))
	})
	assert.Contains(t, out, `"issue_type":"bug"`)
}
