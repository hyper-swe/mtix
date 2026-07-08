// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

func writeHooks(t *testing.T, dir, yaml string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hooks.yaml"), []byte(yaml), 0o600))
}

// TestHooksDispatch_StatusChange_DeliversToInbox: a status.changed hook fires
// and lands in the target agent's inbox — the human-relay-free wake path
// extended beyond addressed comments (MTIX-47.3 / FR-19.3+4).
func TestHooksDispatch_StatusChange_DeliversToInbox(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, `
hooks:
  - name: wake-opus
    match:
      events: [status.changed]
      under: PROJ-1
      to-agent: opus
      status-to: [done]
    deliver: [inbox]
`)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))

	before, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, before, "nothing addressed to opus yet")

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1, "only the done transition matches status-to:[done], not in_progress")
	require.Equal(t, "PROJ-1", got[0].NodeID)
}

// TestHooksDispatch_LateHook_NoBacklog: the dispatcher advances its cursor even
// with no hooks configured, so a hook added later fires only on FUTURE events,
// never a backlog of pre-existing ones.
func TestHooksDispatch_LateHook_NoBacklog(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir() // no hooks.yaml yet

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))

	// Consume the backlog with no hooks — this advances the dispatch cursor.
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	// Now add a broad hook and dispatch again: it must NOT fire on the old events.
	writeHooks(t, dir, `
hooks:
  - name: late
    match: { events: [status.changed], to-agent: opus }
    deliver: [inbox]
`)
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, got, "a hook added after the fact must not fire on the pre-hook backlog")
}
