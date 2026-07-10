// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// TestHooksDispatch_RateLimit: once a hook has fired the per-node cap within the
// window, further firings on that node are skipped — the runaway-loop backstop
// (MTIX-47.7 / FR-19.6).
func TestHooksDispatch_RateLimit(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, `
hooks:
  - name: noisy
    match: { events: [status.changed], to-agent: opus }
    deliver: [inbox]
`)
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "w"})
	require.NoError(t, err)

	// Seed the per-node cap: 20 delivered firings for (noisy, node).
	for i := 0; i < 20; i++ {
		require.NoError(t, store.WriteHookLog(ctx, sqlite.HookLogEntry{
			Hook: "noisy", NodeID: node.ID, Event: "status.changed", Adapter: "inbox", Outcome: "delivered",
		}))
	}

	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "w"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "w"))

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, got, "at the per-node cap, the hook is rate-limited — no further delivery")
}
