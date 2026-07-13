// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// FR-20 / MTIX-56.1: the ledger dispatcher. One dispatch path for every
// trigger; a hook fires for a journaled event based only on the event being in
// the journal and the hook not yet having fired for it on this host — never on
// who wrote it or how it arrived.

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

// TestLedgerDispatch_MixedOrigins_SinglePass: one pass over a journal holding
// both local and synced events fires each matching event exactly once — the
// two-cursor split this replaces could not do this (MTIX-52 → FR-20).
func TestLedgerDispatch_MixedOrigins_SinglePass(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, plainWakeHook)

	makeDoneEvent(t, svc) // becomes "synced" below
	markEventsSynced(t, store)
	makeDoneEvent(t, svc) // stays local

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 2, deliveredCount(t, store, "wake-worker"),
		"one pass fires both the synced and the local done-event, once each")
}

const plainWakeHook = `
hooks:
  - name: wake-worker
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: worker
    deliver: [inbox]
`

// deliveredCount counts hook_log rows recording a real delivery of hook h.
func deliveredCount(t *testing.T, store *sqlite.Store, hook string) int {
	t.Helper()
	entries, err := store.ReadHookLog(context.Background(), 1000)
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		if e.Hook == hook && e.Outcome == "delivered" {
			n++
		}
	}
	return n
}

// makeDoneEvent creates a node and drives it to done, returning nothing; the
// resulting transition_status event is the wake trigger under test.
func makeDoneEvent(t *testing.T, svc *service.NodeService) {
	t.Helper()
	ctx := context.Background()
	node, err := svc.CreateNode(ctx, &service.CreateNodeRequest{Project: "PROJ", Title: "T", Creator: "poster"})
	require.NoError(t, err)
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusInProgress, "", "poster"))
	require.NoError(t, svc.TransitionStatus(ctx, node.ID, model.StatusDone, "", "poster"))
}

// TestLedgerDispatch_SyncedEventFires_NoOptIn: origin-independence (FR-20 G1).
// A sync-arrived event fires a plain hook with NO include-synced opt-in — the
// exact case the MTIX-52 gates suppressed. This is the core reframe.
func TestLedgerDispatch_SyncedEventFires_NoOptIn(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, plainWakeHook)

	makeDoneEvent(t, svc)
	markEventsSynced(t, store) // the events now look like they arrived via the hub

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"),
		"a synced event must fire exactly like a local one — origin is irrelevant")
}

// TestLedgerDispatch_IncludeSyncedIsNoOp: the deprecated flag parses and
// changes nothing — same firing with it, no double-fire because of it.
func TestLedgerDispatch_IncludeSyncedIsNoOp(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, `
hooks:
  - name: wake-worker
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: worker
    include-synced: true
    deliver: [inbox]
`)

	makeDoneEvent(t, svc)
	d := service.NewHooksDispatcher(store, dir, slog.Default())
	d.Dispatch(ctx)
	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"))
}

// TestLedgerDispatch_RestartNoRefire: the ledger survives the dispatcher — a
// fresh instance over the same store never re-fires (FR-20 §10 restart leg).
func TestLedgerDispatch_RestartNoRefire(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, plainWakeHook)

	makeDoneEvent(t, svc)
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)
	// "Restart": a brand-new dispatcher instance over the same store.
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"),
		"a restarted dispatcher must not re-fire a delivered event")
}

// TestLedgerDispatch_CrashBeforeFloorAdvance_NoDoubleFire: a pass that fired
// an event and crashed BEFORE advancing the floor leaves a terminal ledger row
// inside the scan window; the next pass rescans the window and must skip that
// row while firing the rest. This is the ledger doing what a watermark cannot.
func TestLedgerDispatch_CrashBeforeFloorAdvance_NoDoubleFire(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, plainWakeHook)

	makeDoneEvent(t, svc) // event A
	makeDoneEvent(t, svc) // event B

	// Find A's done-transition seq and simulate the crashed pass: A fired
	// (terminal ledger row) but the floor never advanced.
	events, err := store.ReadJournalSince(ctx, 0, 500)
	require.NoError(t, err)
	var doneSeqs []int64
	for _, je := range events {
		evt := service.NormalizeEvent(je)
		if evt.Name == "status.changed" && evt.StatusTo == "done" {
			doneSeqs = append(doneSeqs, je.Seq)
		}
	}
	require.Len(t, doneSeqs, 2)
	won, err := store.ClaimHookDispatch(ctx, "wake-worker", doneSeqs[0], time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, store.RecordHookDispatchOutcome(ctx, "wake-worker", doneSeqs[0], "delivered"))

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"),
		"the rescan fires only B; A's terminal ledger row inside the window is skipped")
}

// TestLedgerDispatch_ConcurrentTriggers_ExactlyOnce: daemon tick, on-commit and
// a second process race the same journal; the PK claim serializes them (§7).
func TestLedgerDispatch_ConcurrentTriggers_ExactlyOnce(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, plainWakeHook)

	makeDoneEvent(t, svc)

	const triggers = 6
	var wg sync.WaitGroup
	for i := 0; i < triggers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)
		}()
	}
	wg.Wait()

	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"),
		"concurrent triggers must resolve to exactly one fire per (hook,event)")
}

// TestLedgerDispatch_CrashBeforeFire_Refires: a stale 'claimed' row (trigger
// crashed between claim and fire) is reclaimed and fired — at-least-once,
// never a lost wake (§7). The claim is even stranded BELOW the scan floor to
// exercise the floor-independent reclaim scan.
func TestLedgerDispatch_CrashBeforeFire_Refires(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, plainWakeHook)

	makeDoneEvent(t, svc)
	tail, err := store.JournalTail(ctx)
	require.NoError(t, err)

	// Simulate the crashed trigger: it claimed the done-event for wake-worker,
	// then died before firing; its claim has outlived the lease.
	won, err := store.ClaimHookDispatch(ctx, "wake-worker", tail, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	stale := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	_, err = store.WriteDB().Exec(
		`UPDATE hook_dispatch_ledger SET fired_at = ? WHERE hook_name = ? AND event_seq = ?`,
		stale, "wake-worker", tail)
	require.NoError(t, err)
	// Strand the claim below the floor (the narrow advance race): the tail
	// scan alone would never revisit it.
	_, err = store.WriteDB().Exec(`
		INSERT INTO hook_dispatch_cursor (id, cursor) VALUES (1, ?)
		ON CONFLICT(id) DO UPDATE SET cursor = excluded.cursor`, tail)
	require.NoError(t, err)

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"),
		"a crashed claim must be reclaimed and fired — a lost wake is the worst outcome")
}

// TestLedgerDispatch_FreshClaimNotStolen: a claim within its lease belongs to
// an in-flight trigger; a concurrent pass must skip it, not double-fire.
func TestLedgerDispatch_FreshClaimNotStolen(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, plainWakeHook)

	makeDoneEvent(t, svc)
	tail, err := store.JournalTail(ctx)
	require.NoError(t, err)
	won, err := store.ClaimHookDispatch(ctx, "wake-worker", tail, time.Minute)
	require.NoError(t, err)
	require.True(t, won)

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 0, deliveredCount(t, store, "wake-worker"),
		"an in-lease claim is owned by another trigger; the pass must skip it")
}

// TestLedgerDispatch_ErrorOutcome_NeverAutoRetried: a fire that RAN and failed
// is terminal (§14.3) — the next pass must not re-fire it.
func TestLedgerDispatch_ErrorOutcome_NeverAutoRetried(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	// Webhook to a port that refuses connections → adapter error → outcome=error.
	writeHooks(t, dir, `
hooks:
  - name: wake-worker
    match:
      events: [status.changed]
      status-to: [done]
    deliver: [webhook]
    webhook:
      url: http://127.0.0.1:1/hook
`)

	makeDoneEvent(t, svc)
	d := service.NewHooksDispatcher(store, dir, slog.Default())
	d.Dispatch(ctx)
	d.Dispatch(ctx) // a second pass must find the terminal error row and skip

	errors := 0
	entries, err := store.ReadHookLog(ctx, 1000)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Hook == "wake-worker" && e.Outcome == "error" {
			errors++
		}
	}
	require.Equal(t, 1, errors, "an errored fire is terminal — never auto-retried")
}

// TestLedgerDispatch_StaleClaimForRemovedHook_AbandonedNotStuck: a stale claim
// whose hook is no longer configured (or whose event cannot be resolved) is
// closed as an error instead of being re-fired forever or parking the scan
// floor for eternity (FR-20 §7).
func TestLedgerDispatch_StaleClaimForRemovedHook_AbandonedNotStuck(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, plainWakeHook)

	makeDoneEvent(t, svc)
	tail, err := store.JournalTail(ctx)
	require.NoError(t, err)

	// A hook that later vanished from hooks.yaml left a stale claim behind.
	won, err := store.ClaimHookDispatch(ctx, "ghost-hook", tail, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	stale := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	_, err = store.WriteDB().Exec(
		`UPDATE hook_dispatch_ledger SET fired_at = ? WHERE hook_name = 'ghost-hook'`, stale)
	require.NoError(t, err)

	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	// The claim was closed as an error (and, being terminal below the advanced
	// floor, promptly pruned); the audit log keeps the durable record.
	entries, err := store.ReadHookLog(ctx, 1000)
	require.NoError(t, err)
	abandoned := 0
	for _, e := range entries {
		if e.Hook == "ghost-hook" && e.Outcome == "error" && e.Detail == "stale claim abandoned" {
			abandoned++
		}
	}
	require.Equal(t, 1, abandoned, "an unresolvable stale claim is closed with an audit entry, not retried forever")

	remaining, err := store.StaleHookClaims(ctx, time.Minute)
	require.NoError(t, err)
	require.Empty(t, remaining, "nothing left to park the scan floor")

	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"),
		"the configured hook still fired normally alongside the abandonment")
}

// TestLedgerDispatch_BootstrapFloorInit: a store whose journal was populated
// before ANY dispatch pass (fresh clone / first pull) initializes its floor at
// the tail — history is never a backlog storm (FR-20 §8). InitHookScanFloor is
// the bootstrap hook the pull path calls after filling an empty store.
func TestLedgerDispatch_BootstrapFloorInit(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()

	makeDoneEvent(t, svc) // "history": events that predate any dispatch state
	require.NoError(t, store.InitHookScanFloorAtTail(ctx))

	writeHooks(t, dir, plainWakeHook)
	service.NewHooksDispatcher(store, dir, slog.Default()).Dispatch(ctx)

	require.Equal(t, 0, deliveredCount(t, store, "wake-worker"),
		"bootstrapped history must arrive pre-dispatched, not as a wake storm")
}
