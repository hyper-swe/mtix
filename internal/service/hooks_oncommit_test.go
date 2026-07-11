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
)

// MTIX-53: a long-running server (the MCP server) fires hooks by wiring
// HooksDispatcher.OnCommitDispatch() as a post-commit callback, so an agent's
// mutation through the server dispatches hooks host-side — the exec cold-start
// path — with no per-command PostRun. The callback MUST be re-entrancy-safe:
// Dispatch itself writes (inbox delivery, cursor, hook log), and those commits
// re-fire on-commit callbacks, so an unguarded dispatch would recurse forever.

// TestOnCommitDispatch_FiresHookOnMutation: with OnCommitDispatch wired as an
// on-commit callback and NO manual Dispatch call, a status change fires the hook
// exactly once. Exactly-once also proves the re-entrancy guard holds — without
// it, Dispatch's own writes would recurse until the stack blows.
func TestOnCommitDispatch_FiresHookOnMutation(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, `
hooks:
  - name: wake-opus
    match: { events: [status.changed], status-to: [done], to-agent: opus }
    deliver: [inbox]
`)

	disp := service.NewHooksDispatcher(store, dir, slog.Default())
	store.AddOnCommit(disp.OnCommitDispatch()) // the only dispatch wiring — no manual Dispatch below

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "w"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "w"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "w"))

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1, "the mutation's on-commit dispatch fired the hook exactly once")
	require.Equal(t, node.ID, got[0].NodeID)
}

// TestOnCommitDispatch_NilDispatcherSafe: a nil dispatcher yields a no-op
// callback (defensive — wiring must never panic a server).
func TestOnCommitDispatch_NilDispatcherSafe(t *testing.T) {
	var d *service.HooksDispatcher
	cb := d.OnCommitDispatch()
	require.NotNil(t, cb)
	require.NotPanics(t, func() { cb() })
}
