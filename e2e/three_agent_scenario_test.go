// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/channel"
	"github.com/hyper-swe/mtix/internal/hooks"
	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
)

// MTIX-56.5b — the three-agent scenario regression guard (FR-20 §10).
//
// The canonical single-host workflow: a PLANNER agent plans a feature and
// hands task work to a DEVELOPER over the inbox; the developer hands the
// result to a TESTER; the tester reports back to the planner. All three run
// on one machine, one .mtix. This test drives every hop through the real
// fabric — journaled addressed comments, the ledger dispatcher, exec wake
// hooks (trusted), and the channel source a live session's push rides on —
// and pins the properties the use case depends on:
//
//   - each handoff cold-start-wakes the addressee's exec hook EXACTLY once;
//   - a live session's channel source yields each event once (no re-push
//     spam), and ack keeps it from resurfacing;
//   - agents without a wake hook (the planner) get inbox delivery, no exec;
//   - a dispatcher restart re-fires nothing;
//   - a trigger that crashed between claim and fire re-fires (never a lost
//     wake).
//
// PG-free: runs in the standard suite on every change, so the planner→
// developer→tester use case cannot silently break.

// commentThread posts addressed comments via the production SetAnnotations
// path. Annotations are CUMULATIVE per node (SetAnnotations replaces the full
// list, and only a NEW entry journals an addressed comment event — matching
// `mtix comment --to`), so the thread carries the growing list.
type commentThread struct {
	cli    *fakeCLI
	nodeID string
	anns   []model.Annotation
}

func (th *commentThread) post(t *testing.T, from, to, text string) {
	t.Helper()
	th.anns = append(th.anns, model.Annotation{
		ID:        fmt.Sprintf("ann-%s-%d", to, len(th.anns)+1),
		Author:    from,
		Text:      text,
		CreatedAt: time.Date(2026, 7, 12, 0, 0, len(th.anns)+1, 0, time.UTC),
		Addressee: to,
	})
	require.NoError(t, th.cli.store.SetAnnotations(context.Background(), th.nodeID, th.anns))
}

// wakeCount reads the record file the wake exec appends to (one line per fire).
func wakeCount(t *testing.T, path string) int {
	t.Helper()
	body, err := os.ReadFile(path) //nolint:gosec // test-owned temp path
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return len(strings.Split(strings.TrimSpace(string(body)), "\n")) - countEmpty(string(body))
}

func countEmpty(body string) int {
	if strings.TrimSpace(body) == "" {
		return 1
	}
	return 0
}

// requireWakes waits for the DETACHED wake exec (MTIX-56.9) to reach exactly n
// firings, then settles briefly and re-checks so an over-fire cannot hide.
func requireWakes(t *testing.T, path string, n int, msg string) {
	t.Helper()
	require.Eventually(t, func() bool { return wakeCount(t, path) == n },
		10*time.Second, 25*time.Millisecond, msg)
	time.Sleep(200 * time.Millisecond)
	require.Equal(t, n, wakeCount(t, path), msg)
}

func TestScenario_PlannerDeveloperTester_HandoffChain(t *testing.T) {
	host := newFakeCLI(t, "planner", "cccccccccccccccc")
	ctx := context.Background()

	// Wake exec: appends the event JSON to a per-agent record file — the
	// observable stand-in for "cold-start the agent's harness CLI".
	recDir := t.TempDir()
	devWakes := filepath.Join(recDir, "developer.wakes")
	testerWakes := filepath.Join(recDir, "tester.wakes")
	script := filepath.Join(recDir, "wake.sh")
	require.NoError(t, os.WriteFile(script,
		[]byte("#!/bin/sh\nprintf '%s\\n' \"$MTIX_EVENT\" >> \"$1\"\n"), 0o700)) //nolint:gosec

	require.NoError(t, os.WriteFile(filepath.Join(host.mtixDir, "hooks.yaml"), []byte(fmt.Sprintf(`
hooks:
  - name: wake-developer
    match:
      events: [comment.addressed]
      to-agent: developer
    deliver: [exec]
    exec:
      command: [%q, %q]
      timeout-seconds: 10
  - name: wake-tester
    match:
      events: [comment.addressed]
      to-agent: tester
    deliver: [exec]
    exec:
      command: [%q, %q]
      timeout-seconds: 10
`, script, devWakes, script, testerWakes)), 0o600))
	// The operator reviews and trusts the config on this host (MTIX-49).
	require.NoError(t, hooks.SaveTrust(host.mtixDir, hooks.ConfigHash(host.mtixDir)))

	disp := service.NewHooksDispatcher(host.store, host.mtixDir, e2eLogger())
	host.createNode(t, "FLOW-1", "the planned feature task")
	thread := &commentThread{cli: host, nodeID: "FLOW-1"}

	// ---- hop 1: planner → developer -------------------------------------
	thread.post(t, "planner", "developer", "Start on FLOW-1. Plan context attached.")
	disp.Dispatch(ctx)
	requireWakes(t, devWakes, 1, "the developer wake exec fired for the handoff")
	requireWakes(t, testerWakes, 0, "nothing addressed the tester yet")

	// A live developer session's channel source sees the event exactly once.
	devSrc := channel.NewInboxSource(host.store, "developer")
	pushed, err := devSrc.Next(ctx, 10*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, pushed, 1)
	assert.Equal(t, "FLOW-1", pushed[0].Node)
	assert.Equal(t, "planner", pushed[0].From)
	assert.Contains(t, pushed[0].Body, "Start on FLOW-1")
	again, err := devSrc.Next(ctx, 10*time.Millisecond)
	require.NoError(t, err)
	assert.Empty(t, again, "the same event is never re-pushed into a live session")

	// The developer handles the work and acks — the event stops resurfacing.
	require.NoError(t, host.store.InboxAck(ctx, "developer", pushed[0].Seq))
	left, err := host.store.InboxList(ctx, "developer")
	require.NoError(t, err)
	assert.Empty(t, left, "ack clears the developer's inbox")

	// ---- hop 2: developer → tester --------------------------------------
	thread.post(t, "developer", "tester", "FLOW-1 ready for verification.")
	disp.Dispatch(ctx)
	requireWakes(t, testerWakes, 1, "the tester wake exec fired for the handoff")
	requireWakes(t, devWakes, 1, "the developer hook did not re-fire")

	testerSrc := channel.NewInboxSource(host.store, "tester")
	tPushed, err := testerSrc.Next(ctx, 10*time.Millisecond)
	require.NoError(t, err)
	require.Len(t, tPushed, 1)
	require.NoError(t, host.store.InboxAck(ctx, "tester", tPushed[0].Seq))

	// ---- hop 3: tester → planner (no wake hook: inbox-only delivery) ----
	thread.post(t, "tester", "planner", "FLOW-1 verified. Closing the loop.")
	disp.Dispatch(ctx)
	plannerInbox, err := host.store.InboxList(ctx, "planner")
	require.NoError(t, err)
	require.Len(t, plannerInbox, 1, "the planner sees the verification report")
	requireWakes(t, devWakes, 1, "no stray developer wake")
	requireWakes(t, testerWakes, 1, "no stray tester wake")

	// ---- restart: a fresh dispatcher re-fires nothing --------------------
	service.NewHooksDispatcher(host.store, host.mtixDir, e2eLogger()).Dispatch(ctx)
	requireWakes(t, devWakes, 1, "no re-fire")
	requireWakes(t, testerWakes, 1, "no re-fire")

	// ---- crash injection: claim-then-die is re-fired, never lost ---------
	thread.post(t, "planner", "developer", "Round two: address review notes.")
	tail, err := host.store.JournalTail(ctx)
	require.NoError(t, err)
	won, err := host.store.ClaimHookDispatch(ctx, "wake-developer", tail, time.Minute)
	require.NoError(t, err)
	require.True(t, won, "simulate a trigger that claimed the handoff")
	stale := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	_, err = host.store.WriteDB().Exec(
		`UPDATE hook_dispatch_ledger SET fired_at = ? WHERE hook_name = 'wake-developer' AND event_seq = ?`,
		stale, tail)
	require.NoError(t, err) // ...and died before firing, past the lease

	disp.Dispatch(ctx)
	requireWakes(t, devWakes, 2,
		"the crashed trigger's wake is re-fired — a lost wake is the failure this fabric exists to kill")
	requireWakes(t, testerWakes, 1, "no re-fire")
}
