// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// FR-19 (MTIX-47.10) cloud/sync end-to-end. These prove the two team-mode
// properties that only surface once events cross the hub — the local unit tests
// (internal/hooks, internal/service) cannot exercise them because they never
// replicate an event through Postgres:
//
//  1. The inbox is a QUERY over the replicated sync_events journal, so an
//     addressed comment made on machine A appears in machine B's inbox after a
//     normal push/pull — no separate mailbox, no extra delivery step.
//  2. Hook dispatch is ORIGIN-INDEPENDENT with per-host exactly-once (FR-20):
//     an event fires the hooks CONFIGURED ON A HOST no matter where the event
//     was written, deduped per (hook,event) by that host's dispatch ledger.
//     Fleet-level "who fires what" is hook PLACEMENT: a hook present on N
//     hosts fires on N hosts, once each; a host without the hook never fires
//     it. That is how a wake exec runs where the workers live, not on
//     whichever machine posted the event.
//
// Gated on MTIX_PG_TEST_DSN via openHub (skips when unset), like the other
// e2e sync tests.

// addressedComment emits a comment addressed at an agent, via the production
// SetAnnotations path, so the resulting comment sync_event carries payload.to
// exactly as the real CLI's `mtix comment --to` would.
func (c *fakeCLI) addressedComment(t *testing.T, nodeID, to, text string) {
	t.Helper()
	require.NoError(t, c.store.SetAnnotations(context.Background(), nodeID, []model.Annotation{{
		ID:        "ann-" + to,
		Author:    c.name,
		Text:      text,
		CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC),
		Addressee: to,
	}}), "addressedComment %s->%s on %s", nodeID, to, c.name)
}

func e2eLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

// TestE2E_FR19_AddressedComment_ReplicatesToReceiverInbox: a comment addressed
// at "opus" on machine A lands in machine B's inbox after A pushes and B pulls.
// The inbox is derived from the replicated journal — B never received a
// separate mailbox message, just the ordinary comment event.
func TestE2E_FR19_AddressedComment_ReplicatesToReceiverInbox(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")

	// A shared node exists on both machines (created on A, replicated to B) so
	// B can apply the comment event against a real node.
	a.createNode(t, "HOOK-1", "shared node")
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)

	// Before the comment replicates, B's inbox for opus is empty.
	pre, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, pre, "no addressed events for opus on B yet")

	// A addresses a comment at opus, then push -> hub -> pull to B.
	a.addressedComment(t, "HOOK-1", "opus", "ruling: proceed to phase 2")
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)

	got, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, got, 1, "the addressed comment replicated into B's inbox")
	require.Equal(t, "HOOK-1", got[0].NodeID)
	require.Equal(t, "ruling: proceed to phase 2", got[0].Body)

	// A comment addressed at someone else does not leak into opus's inbox on B.
	other, err := b.store.InboxList(ctx, "sonnet")
	require.NoError(t, err)
	require.Empty(t, other, "opus's comment is not visible to a different addressee")
}

// TestE2E_FR20_SharedHook_FiresOncePerConfiguredHost: both machines carry the
// same hook (committed hooks.yaml — the shared-config case). A status change
// made on A fires A's hook once; after replication B's hook fires once on B
// too — placement is designation (FR-20 §5), and the per-host ledger keeps
// each host at exactly-once. Repeat dispatch on either host never re-fires.
func TestE2E_FR20_SharedHook_FiresOncePerConfiguredHost(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")

	// Both machines carry the SAME hook config — the realistic team case where
	// hooks.yaml is committed and shared. Each configured host fires once.
	hookYAML := `
hooks:
  - name: wake-on-done
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: opus
    deliver: [inbox]
`
	require.NoError(t, os.WriteFile(filepath.Join(a.mtixDir, "hooks.yaml"), []byte(hookYAML), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(b.mtixDir, "hooks.yaml"), []byte(hookYAML), 0o600))

	// Shared node on both machines.
	a.createNode(t, "HOOK-2", "task")
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)

	// A drives the node to done (open -> in_progress -> done; the state machine
	// forbids a direct jump). Only the `done` transition matches the hook.
	require.NoError(t, a.store.TransitionStatus(ctx, "HOOK-2", model.StatusInProgress, "", "worker"))
	require.NoError(t, a.store.TransitionStatus(ctx, "HOOK-2", model.StatusDone, "", "worker"))

	// Origin dispatch: the done event is local (not synced) -> the hook fires.
	service.NewHooksDispatcher(a.store, a.mtixDir, e2eLogger()).Dispatch(ctx)

	originInbox, err := a.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, originInbox, 1, "the hook fires once on the origin machine")
	require.Equal(t, "HOOK-2", originInbox[0].NodeID)

	// Replicate the transitions to B, then dispatch on B. B carries the same
	// hook, so B fires it for the synced event — once, on B's own ledger.
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	bDisp := service.NewHooksDispatcher(b.store, b.mtixDir, e2eLogger())
	bDisp.Dispatch(ctx)

	replicaInbox, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, replicaInbox, 1, "B configured the hook too, so B fires it for the synced event — placement is designation")
	require.Equal(t, "HOOK-2", replicaInbox[0].NodeID)

	// Exactly-once per host: repeat passes on both machines change nothing.
	service.NewHooksDispatcher(a.store, a.mtixDir, e2eLogger()).Dispatch(ctx)
	bDisp.Dispatch(ctx)
	originAgain, err := a.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, originAgain, 1, "no re-fire on the origin")
	replicaAgain, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, replicaAgain, 1, "no re-fire on the replica")
}

// TestE2E_FR20_HookPlacement_FiresOnWorkerHostOnly is the FR-20 §10 acceptance
// core: a hook placed ONLY on the worker host fires there — exactly once — for
// an event that originated on another machine, with no opt-in flag and no
// special dispatch path; the poster's host, which does not configure the hook,
// never fires it. This is the multi-machine wake chain (exec fires where the
// workers live, not on whichever machine posted the event).
func TestE2E_FR20_HookPlacement_FiresOnWorkerHostOnly(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa") // origin (the poster's machine)
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb") // worker host (the hook lives here)

	// A plain hook — no include-synced, no designation. Placement on B is the
	// whole configuration.
	hookYAML := `
hooks:
  - name: wake-on-done
    match:
      events: [status.changed]
      status-to: [done]
      to-agent: opus
    deliver: [inbox]
`
	require.NoError(t, os.WriteFile(filepath.Join(b.mtixDir, "hooks.yaml"), []byte(hookYAML), 0o600))

	// A creates the node and drives it to done, then it replicates to B.
	a.createNode(t, "HOOK-3", "task")
	require.NoError(t, a.store.TransitionStatus(ctx, "HOOK-3", model.StatusInProgress, "", "worker"))
	require.NoError(t, a.store.TransitionStatus(ctx, "HOOK-3", model.StatusDone, "", "worker"))
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)

	// A has no hook configured, so dispatch on A fires nothing.
	service.NewHooksDispatcher(a.store, a.mtixDir, e2eLogger()).Dispatch(ctx)
	onOrigin, err := a.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, onOrigin, "the poster's host does not configure the hook and never fires it")

	// One ordinary dispatch pass on B fires the hook for A's event: origin is
	// irrelevant, placement decides.
	disp := service.NewHooksDispatcher(b.store, b.mtixDir, e2eLogger())
	disp.Dispatch(ctx)
	afterDispatch, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, afterDispatch, 1, "the worker host fires its hook for the cross-machine event")
	require.Equal(t, "HOOK-3", afterDispatch[0].NodeID)

	// A second pass does not re-fire — the ledger holds the (hook,event) claim.
	disp.Dispatch(ctx)
	afterSecond, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, afterSecond, 1, "dispatch is exactly-once per host; a second pass does not re-fire")
}

// TestE2E_FR20_ReleaseChain_ExecWake is the FR-20 §10 RELEASE GATE: a poster
// on machine A addresses a worker; the worker host B's dispatcher fires the
// wake EXEC exactly once for A's event; A never fires it; a restart re-fires
// nothing; a trigger that crashed between claim and fire re-fires (a double
// wake is acceptable, a lost wake is not). Green = the cold-start fabric
// works headless, cross-machine, zero human touches.
func TestE2E_FR20_ReleaseChain_ExecWake(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa") // poster's machine
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb") // worker host

	// The wake exec records each invocation — the observable stand-in for
	// launching the worker's harness CLI with the inbox as its prompt.
	recDir := t.TempDir()
	wakes := filepath.Join(recDir, "worker.wakes")
	script := filepath.Join(recDir, "wake.sh")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nprintf '%s\\n' \"$MTIX_EVENT\" >> \""+wakes+"\"\n"), 0o700)) //nolint:gosec

	hookYAML := fmt.Sprintf(`
hooks:
  - name: wake-worker
    match:
      events: [comment.addressed]
      to-agent: worker
    deliver: [exec]
    exec:
      command: [%q]
      timeout-seconds: 10
`, script)
	require.NoError(t, os.WriteFile(filepath.Join(b.mtixDir, "hooks.yaml"), []byte(hookYAML), 0o600))
	require.NoError(t, hooks.SaveTrust(b.mtixDir, hooks.ConfigHash(b.mtixDir)))

	// Shared node, then the cross-machine handoff. Comments ride a cumulative
	// annotation thread (SetAnnotations replaces the list; only a NEW entry
	// journals an addressed comment event).
	a.createNode(t, "GATE-1", "work for the worker")
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	thread := &commentThread{cli: a, nodeID: "GATE-1"}
	thread.post(t, "poster", "worker", "begin: acceptance chain")
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)

	// A configures no hook and fires nothing, whatever it dispatches.
	service.NewHooksDispatcher(a.store, a.mtixDir, e2eLogger()).Dispatch(ctx)
	requireWakeLines(t, wakes, 0, "the poster's machine never runs the wake exec")

	// B's dispatcher (what its daemon runs every tick) fires it exactly once.
	disp := service.NewHooksDispatcher(b.store, b.mtixDir, e2eLogger())
	disp.Dispatch(ctx)
	requireWakeLines(t, wakes, 1, "the worker host fires the wake exec for the cross-machine event")
	disp.Dispatch(ctx)
	requireWakeLines(t, wakes, 1, "a second tick re-fires nothing (ledger)")

	// Restart: a brand-new dispatcher over the same store re-fires nothing.
	service.NewHooksDispatcher(b.store, b.mtixDir, e2eLogger()).Dispatch(ctx)
	requireWakeLines(t, wakes, 1, "restart-safe")

	// Crash injection: a second handoff is claimed by a trigger that dies
	// before firing; the stale claim is reclaimed and fired — never lost.
	thread.post(t, "poster", "worker", "round two")
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	tail, err := b.store.JournalTail(ctx)
	require.NoError(t, err)
	won, err := b.store.ClaimHookDispatch(ctx, "wake-worker", tail, time.Minute)
	require.NoError(t, err)
	require.True(t, won)
	stale := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	_, err = b.store.WriteDB().Exec(
		`UPDATE hook_dispatch_ledger SET fired_at = ? WHERE hook_name = 'wake-worker' AND event_seq = ?`,
		stale, tail)
	require.NoError(t, err)

	disp.Dispatch(ctx)
	requireWakeLines(t, wakes, 2, "the crashed trigger's wake is re-fired, not lost (at-least-once)")
}

// requireWakeLines asserts the wake record file settles at exactly n lines
// (one line per exec invocation; a missing file is zero). The exec spawn is
// DETACHED (MTIX-56.9), so reaching n is awaited, then re-checked after a
// short settle so an over-fire cannot hide.
func requireWakeLines(t *testing.T, path string, n int, msg string) {
	t.Helper()
	count := func() int {
		body, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
		if err != nil {
			return 0
		}
		trimmed := strings.TrimSpace(string(body))
		if trimmed == "" {
			return 0
		}
		return len(strings.Split(trimmed, "\n"))
	}
	require.Eventually(t, func() bool { return count() == n },
		10*time.Second, 25*time.Millisecond, msg)
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, n, count(), msg)
}

// TestE2E_FR20_ServerOnCommitDispatch_CoversEveryOrigin: when a long-running
// server wires OnCommitDispatch (MTIX-53), a LOCAL mutation fires its hook
// immediately — and applying SYNCED events pulled from the hub fires the hook
// too, through the very same on-commit trigger (FR-20: one dispatch path, any
// origin, no daemon required on a serve-only host). Each fires exactly once —
// the ledger dedupes; there is no team-wide duplicate because only hosts that
// CONFIGURE a hook fire it (placement, §5).
func TestE2E_FR20_ServerOnCommitDispatch_CoversEveryOrigin(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa") // another machine posting work
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb") // the server host (MCP/serve) with hook dispatch wired

	hookYAML := `
hooks:
  - name: wake-opus-on-done
    match: { events: [status.changed], status-to: [done], to-agent: opus }
    deliver: [inbox]
`
	require.NoError(t, os.WriteFile(filepath.Join(b.mtixDir, "hooks.yaml"), []byte(hookYAML), 0o600))

	// Wire hook dispatch on B's store exactly as runMCP/runServe do — a
	// post-commit callback that runs the local Dispatch, re-entrancy-guarded.
	disp := service.NewHooksDispatcher(b.store, b.mtixDir, e2eLogger())
	b.store.AddOnCommit(disp.OnCommitDispatch())

	// LOCAL mutation on the server host: the on-commit dispatch fires the hook.
	b.createNode(t, "HOOK-4", "local task")
	require.NoError(t, b.store.TransitionStatus(ctx, "HOOK-4", model.StatusInProgress, "", "worker"))
	require.NoError(t, b.store.TransitionStatus(ctx, "HOOK-4", model.StatusDone, "", "worker"))

	afterLocal, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, afterLocal, 1, "a local mutation on the server host fires the hook via on-commit dispatch")
	require.Equal(t, "HOOK-4", afterLocal[0].NodeID)

	// A posts equivalent work that replicates to B. Applying those SYNCED
	// events on B triggers the same on-commit callback, and under FR-20 the
	// hook fires for them too — cross-machine work wakes the worker host even
	// with no daemon running, exactly once per event.
	a.createNode(t, "HOOK-5", "remote task")
	require.NoError(t, a.store.TransitionStatus(ctx, "HOOK-5", model.StatusInProgress, "", "worker"))
	require.NoError(t, a.store.TransitionStatus(ctx, "HOOK-5", model.StatusDone, "", "worker"))
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool) // apply commit fires B's on-commit dispatch over the synced events

	afterSync, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Len(t, afterSync, 2, "the sync-arrived done event fires the hook via on-commit — origin-independent, no daemon needed")
	nodes := []string{afterSync[0].NodeID, afterSync[1].NodeID}
	require.ElementsMatch(t, []string{"HOOK-4", "HOOK-5"}, nodes, "both the local and the cross-machine event fired, once each")
}
