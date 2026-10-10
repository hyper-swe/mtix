// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Hook exec-policy tests verify asynchronous spawn ordering and host policy
// with a process barrier, rather than a mutation latency threshold.
package service_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-56.9 (detached exec) + MTIX-56.10 (exec dispatch-host policy).

func recCount(path string) int {
	body, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
	if err != nil {
		return 0
	}
	n := 0
	for _, b := range body {
		if b == '\n' {
			n++
		}
	}
	return n
}

// TestExecDispatch_DetachedSpawn_DoesNotBlockMutationPath: a slow exec hook
// must not stall Dispatch (the FR-19 async contract, MTIX-56.9). The ledger
// outcome is 'delivered' at spawn.
func TestExecDispatch_DetachedSpawn_DoesNotBlockMutationPath(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	gate := newExecHookBarrier(t)
	makeDoneEvent(t, svc)
	dispatch := service.NewHooksDispatcher(store, gate.dir, slog.Default())
	gate.observeDispatch(t, dispatch)
	gate.dispatchBeforeRelease(t, func() { dispatch.Dispatch(ctx) })

	// Spawn success is recorded while the hook is still parked. Completion
	// is impossible until this test releases the hook after Dispatch returns.
	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"),
		"delivered == spawned, recorded before the hook completes")
	require.Zero(t, recCount(gate.record), "the parked hook has not completed")
	gate.complete(t)
	require.Equal(t, 1, recCount(gate.record), "the detached hook completes exactly once")
}

// TestExecDispatch_SpawnFailureIsError: a spawn that cannot start (missing
// script) records outcome=error — terminal, never auto-retried.
func TestExecDispatch_SpawnFailureIsError(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	writeHooks(t, dir, `
hooks:
  - name: wake-worker
    match: { events: [status.changed], status-to: [done] }
    deliver: [exec]
    exec: { command: ["`+filepath.Join(dir, "does-not-exist.sh")+`"], timeout-seconds: 5 }
`)
	require.NoError(t, hooks.SaveTrust(dir, hooks.ConfigHash(dir)))

	makeDoneEvent(t, svc)
	d := service.NewHooksDispatcher(store, dir, slog.Default())
	d.Dispatch(ctx)
	d.Dispatch(ctx) // no retry of the terminal error

	errors := 0
	entries, err := store.ReadHookLog(ctx, 100)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Hook == "wake-worker" && e.Outcome == "error" {
			errors++
		}
	}
	require.Equal(t, 1, errors, "spawn failure is terminal error, never auto-retried")
}

// TestExecPolicy_DaemonMode_NonDaemonTriggerDefersEntirely: under
// exec-dispatch=daemon a CLI/server dispatcher claims NOTHING and moves
// NOTHING — the daemon-marked dispatcher then fires (MTIX-56.10). This is what
// prevents a non-daemon trigger from consuming a wake the daemon should run.
func TestExecPolicy_DaemonMode_NonDaemonTriggerDefersEntirely(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	gate := newExecHookBarrier(t)
	dir := gate.dir
	require.NoError(t, hooks.SaveExecDispatchMode(dir, hooks.ExecDispatchDaemon))

	makeDoneEvent(t, svc)

	// CLI-shaped trigger: full no-op — no ledger rows, floor unchanged.
	nonDaemon := service.NewHooksDispatcher(store, dir, slog.Default())
	gate.observeDispatch(t, nonDaemon)
	nonDaemon.Dispatch(ctx)
	require.Zero(t, gate.deliveries.Load(), "a non-daemon trigger must invoke no exec adapter")
	floor, err := store.HookCursor(ctx)
	require.NoError(t, err)
	assert.Zero(t, floor, "a non-daemon trigger must not advance the shared floor under daemon policy")
	var rows int
	require.NoError(t, store.ReadDB().QueryRow(`SELECT COUNT(*) FROM hook_dispatch_ledger`).Scan(&rows))
	assert.Zero(t, rows, "…and must claim nothing the daemon should fire")

	// The daemon-marked dispatcher fires it.
	daemon := service.NewHooksDispatcher(store, dir, slog.Default())
	daemon.MarkDaemon()
	gate.observeDispatch(t, daemon)
	gate.dispatchBeforeRelease(t, func() { daemon.Dispatch(ctx) })
	daemon.Dispatch(ctx) // The same journal entry must not spawn another hook.
	require.Equal(t, int64(1), gate.deliveries.Load(), "replay must not invoke the exec adapter again")
	require.Equal(t, 1, deliveredCount(t, store, "wake-worker"))
	require.Zero(t, recCount(gate.record), "the hook remains parked until released")
	gate.complete(t)
	require.Equal(t, 1, recCount(gate.record), "the daemon trigger fires the wake exactly once")
}

// TestExecPolicy_OffMode_SkipsExecKeepsOtherAdapters: exec-dispatch=off makes
// this host never launch anything; inbox delivery still happens; the outcome
// is the terminal skipped-policy.
func TestExecPolicy_OffMode_SkipsExecKeepsOtherAdapters(t *testing.T) {
	svc, store, _ := newTestNodeService(t)
	ctx := context.Background()
	dir := t.TempDir()
	rec := filepath.Join(dir, "rec")
	script := filepath.Join(dir, "wake.sh")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nprintf 'fired\\n' >> \""+rec+"\"\n"), 0o700)) //nolint:gosec
	writeHooks(t, dir, `
hooks:
  - name: wake-worker
    match: { events: [status.changed], status-to: [done], to-agent: worker }
    deliver: [inbox, exec]
    exec: { command: ["`+script+`"], timeout-seconds: 5 }
`)
	require.NoError(t, hooks.SaveTrust(dir, hooks.ConfigHash(dir)))
	require.NoError(t, hooks.SaveExecDispatchMode(dir, hooks.ExecDispatchOff))

	makeDoneEvent(t, svc)
	d := service.NewHooksDispatcher(store, dir, slog.Default())
	d.Dispatch(ctx)
	d.Dispatch(ctx) // skipped-policy is terminal — no exec on later passes either

	inbox, err := store.InboxList(ctx, "worker")
	require.NoError(t, err)
	require.Len(t, inbox, 1, "inbox delivery is unaffected by the exec policy")
	assert.Zero(t, recCount(rec), "this host never launches anything under off")
	skipped := 0
	entries, err := store.ReadHookLog(ctx, 100)
	require.NoError(t, err)
	for _, e := range entries {
		if e.Hook == "wake-worker" && e.Outcome == sqlite.OutcomeSkippedPolicy {
			skipped++
		}
	}
	assert.Equal(t, 1, skipped, "exec skipped once with the terminal skipped-policy outcome")
}
