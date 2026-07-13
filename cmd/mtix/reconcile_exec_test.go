// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

// TestRunReconcileDryRun renders the reconcile preview without mutating —
// covers runReconcileDryRun + printReconcilePlan for the local escape paths.
func TestRunReconcileDryRun(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	_, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "n", Creator: "u"})
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReconcileDryRun(ctx, &stdout, &stderr, reconcileFlags{renameTo: "NEWPFX", dryRun: true}))
	assert.NotEmpty(t, stdout.String(), "the plan is rendered")

	// A flagless dry-run surfaces the no-path error.
	require.Error(t, runReconcileDryRun(ctx, &stdout, &stderr, reconcileFlags{}))
}

// TestRunReconcileExecute_DiscardLocal executes the local discard-local escape
// (no hub needed) and reports completion.
func TestRunReconcileExecute_DiscardLocal(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	_, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "n", Creator: "u"})
	require.NoError(t, err)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runReconcileExecute(ctx, &stdout, &stderr, reconcileFlags{discardLocal: true, yes: true}))
	assert.Contains(t, stdout.String(), "discard-local complete")
}
