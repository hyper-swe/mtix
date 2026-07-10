// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

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
//  2. Hooks fire on the ORIGIN only. A status change made on A fires A's hook
//     once; when the same event replicates to B it is marked synced (present in
//     applied_events), and the `Synced && !IncludeSynced` guard (match.go:20)
//     keeps B's hook from re-firing. Without this, every teammate that pulled a
//     status change would re-deliver it — a team-wide duplicate-notification
//     storm. This is the guardrail against exactly that.
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

// TestE2E_FR19_SyncedStatusChange_FiresHookOnOriginOnly: a status change made
// on A fires A's status.changed hook exactly once; when the same event
// replicates to B it is synced, so B's identical hook does NOT re-fire. This is
// the end-to-end proof of the synced-event guardrail against team-wide
// duplicate firing.
func TestE2E_FR19_SyncedStatusChange_FiresHookOnOriginOnly(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")

	// Both machines carry the SAME hook config — the realistic team case where
	// hooks.yaml is committed and shared. Only the origin should fire.
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

	// Replicate the transitions to B, then dispatch on B. On B the done event is
	// present in applied_events -> synced -> the guard keeps the hook from firing.
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	service.NewHooksDispatcher(b.store, b.mtixDir, e2eLogger()).Dispatch(ctx)

	replicaInbox, err := b.store.InboxList(ctx, "opus")
	require.NoError(t, err)
	require.Empty(t, replicaInbox, "the synced status change does NOT re-fire the hook on the replica")

	// Guard the guardrail: confirm the done event really did replicate to B (so
	// the empty inbox is the synced-skip, not a missing event). B's journal must
	// carry the transition, marked synced.
	var syncedDone int
	require.NoError(t, b.store.ReadDB().QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sync_events e
		JOIN applied_events a ON a.event_id = e.event_id
		WHERE e.node_id = 'HOOK-2' AND e.op_type = 'transition_status'
		  AND json_extract(e.payload, '$.to') = 'done'`).Scan(&syncedDone))
	require.Equal(t, 1, syncedDone, "the done transition replicated to B and is marked synced")
}
