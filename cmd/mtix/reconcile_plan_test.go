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

// TestComputePlan covers the reconcile dry-run planner's branches: each
// escape path returns a plan, and no path selected is an error.
func TestComputePlan(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()
	_, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "n", Creator: "u"})
	require.NoError(t, err)

	_, err = computePlan(ctx, app.store, reconcileFlags{discardLocal: true})
	require.NoError(t, err, "discard-local produces a plan")

	_, err = computePlan(ctx, app.store, reconcileFlags{renameTo: "NEWPFX"})
	require.NoError(t, err, "rename-to produces a plan")

	_, err = computePlan(ctx, app.store, reconcileFlags{})
	require.Error(t, err, "no path selected is an error")
	assert.Contains(t, err.Error(), "no path")
}
