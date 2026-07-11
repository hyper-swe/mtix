// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// MTIX-53: wireHookDispatch registers hook dispatch on the store's on-commit
// path, so a long-running server (MCP, serve) fires hooks on every mutation
// host-side with no per-command PostRunE. This proves the wiring end-to-end:
// after wireHookDispatch, a mutation made straight through the service (NOT via
// cobra, so PersistentPostRunE never runs) fires the hook.
package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func TestWireHookDispatch_FiresHookOnServerMutation(t *testing.T) {
	initTestApp(t)
	ctx := context.Background()

	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "hooks.yaml"), []byte(`
hooks:
  - name: wake-opus
    match: { events: [status.changed], status-to: [done], to-agent: opus }
    deliver: [inbox]
`), 0o600))

	// The wiring under test — the same call runMCP / runServe make.
	wireHookDispatch()

	node, err := app.nodeSvc.CreateNode(ctx, &service.CreateNodeRequest{Project: "TEST", Title: "T", Creator: "w"})
	require.NoError(t, err)
	require.NoError(t, app.nodeSvc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "w"))
	require.NoError(t, app.nodeSvc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "w"))

	// No manual Dispatch call — the on-commit wiring must have fired the hook.
	got, err := app.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1, "a server-side mutation dispatched the hook via the on-commit wiring")
	require.Equal(t, node.ID, got[0].NodeID)
}
