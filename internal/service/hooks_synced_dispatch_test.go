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

// markEventsSynced inserts every current sync_events row into applied_events, so
// ReadJournalSince reports them as Synced=true — the in-test equivalent of those
// events having arrived via the hub on another machine.
func markEventsSynced(t *testing.T, store *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	ids := func() []string {
		rows, err := store.ReadDB().QueryContext(ctx, `SELECT event_id FROM sync_events`)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		var out []string
		for rows.Next() {
			var id string
			require.NoError(t, rows.Scan(&id))
			out = append(out, id)
		}
		require.NoError(t, rows.Err())
		return out
	}()
	for _, id := range ids {
		_, err := store.WriteDB().ExecContext(ctx,
			`INSERT OR IGNORE INTO applied_events (event_id, applied_at, applied_by_lamport)
			 VALUES (?, '2026-05-01T00:00:00Z', 1)`, id)
		require.NoError(t, err)
	}
}

const syncedWakeHook = `
hooks:
  - name: wake-on-synced-done
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: opus
    include-synced: true
    deliver: [inbox]
`

// TestHooksDispatch_LocalMode_NeverFiresSyncedEvents: the default (local)
// dispatch path — the CLI post-command path on every host — MUST NOT fire on a
// synced event, even for an include-synced hook. Otherwise every machine that
// pulled the event would re-deliver it (team-wide duplicate firing). Only the
// designated host's synced-dispatch path (below) fires synced events. (MTIX-52.1)
func TestHooksDispatch_LocalMode_NeverFiresSyncedEvents(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, syncedWakeHook)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))
	markEventsSynced(t, store) // the events now look like they arrived via the hub

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, got, "local dispatch must never fire a synced event, even for an include-synced hook")
}

// TestHooksDispatch_SyncedMode_FiresIncludeSyncedOnSyncedEvent: the designated
// host's synced-dispatch path fires an include-synced hook on a synced event —
// the multi-machine wake path (MTIX-52.1).
func TestHooksDispatch_SyncedMode_FiresIncludeSyncedOnSyncedEvent(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, syncedWakeHook)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))
	markEventsSynced(t, store)

	service.NewHooksDispatcher(store, dir, slog.Default()).DispatchSynced(ctx)

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1, "the designated synced-dispatch fires the include-synced hook once")
	require.Equal(t, node.ID, got[0].NodeID)
}

// TestHooksDispatch_SyncedMode_IgnoresLocalEvents: synced-dispatch fires ONLY on
// synced events; local (origin) events remain the local path's job, so the
// designated host does not double-fire a hook the origin already fired.
func TestHooksDispatch_SyncedMode_IgnoresLocalEvents(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, syncedWakeHook)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))
	// NOT marked synced — these are local events.

	service.NewHooksDispatcher(store, dir, slog.Default()).DispatchSynced(ctx)

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, got, "synced-dispatch ignores local events; the local path owns them")
}

// TestHooksDispatch_SyncedMode_RespectsIncludeSyncedOptIn: a synced event does
// NOT fire a hook that did not opt in via include-synced — the guard still holds
// on the designated path; opting in is explicit.
func TestHooksDispatch_SyncedMode_RespectsIncludeSyncedOptIn(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, `
hooks:
  - name: no-optin
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: opus
    deliver: [inbox]
`)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))
	markEventsSynced(t, store)

	service.NewHooksDispatcher(store, dir, slog.Default()).DispatchSynced(ctx)

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, got, "without include-synced, a synced event does not fire even on the designated path")
}

// TestHooksDispatch_SeparateCursors_LocalAdvanceDoesNotConsumeSynced: on the
// designated host, the CLI-local dispatch runs too and advances the LOCAL cursor
// past synced events (which it skips). That must NOT prevent the synced-dispatch
// path — which owns a SEPARATE cursor — from later firing them. This is why the
// two paths cannot share one cursor (MTIX-52.1).
func TestHooksDispatch_SeparateCursors_LocalAdvanceDoesNotConsumeSynced(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, syncedWakeHook)

	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "worker"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "worker"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "worker"))
	markEventsSynced(t, store)

	disp := service.NewHooksDispatcher(store, dir, slog.Default())
	disp.Dispatch(ctx)       // local path sails past the synced events (fires none)
	disp.DispatchSynced(ctx) // synced path must still see and fire them

	got, err := store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1, "the local cursor advance must not consume events from the synced path's cursor")
}
