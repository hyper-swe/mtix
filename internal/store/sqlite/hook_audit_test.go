// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// The hook audit/delivery/rate-limit store primitives the dispatch fabric
// relies on (FR-19.4/19.6/19.7). Unit-tested directly here rather than only
// through the dispatcher.

// TestWriteReadHookLog: firings round-trip newest-first and honor the limit.
func TestWriteReadHookLog(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for _, e := range []sqlite.HookLogEntry{
		{Hook: "wake", NodeID: "P-1", Event: "comment.addressed", Adapter: "exec", Outcome: "delivered"},
		{Hook: "wake", NodeID: "P-2", Event: "comment.addressed", Adapter: "exec", Outcome: "error", Detail: "boom"},
		{Hook: "notify", NodeID: "P-3", Event: "status.changed", Adapter: "webhook", Outcome: "delivered"},
	} {
		require.NoError(t, s.WriteHookLog(ctx, e))
	}

	all, err := s.ReadHookLog(ctx, 50)
	require.NoError(t, err)
	require.Len(t, all, 3)
	assert.Equal(t, "notify", all[0].Hook, "newest first")
	assert.Equal(t, "error", all[1].Outcome)
	assert.Equal(t, "boom", all[1].Detail)

	limited, err := s.ReadHookLog(ctx, 1)
	require.NoError(t, err)
	require.Len(t, limited, 1, "the limit is honored")
}

// TestHookFiringCount: counts only DELIVERED firings of a hook on a node at or
// after the cutoff — the per-node rate-limit input (FR-19.6).
func TestHookFiringCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.WriteHookLog(ctx, sqlite.HookLogEntry{Hook: "wake", NodeID: "P-1", Event: "e", Adapter: "exec", Outcome: "delivered"}))
	require.NoError(t, s.WriteHookLog(ctx, sqlite.HookLogEntry{Hook: "wake", NodeID: "P-1", Event: "e", Adapter: "exec", Outcome: "delivered"}))
	require.NoError(t, s.WriteHookLog(ctx, sqlite.HookLogEntry{Hook: "wake", NodeID: "P-1", Event: "e", Adapter: "exec", Outcome: "error"}))
	require.NoError(t, s.WriteHookLog(ctx, sqlite.HookLogEntry{Hook: "wake", NodeID: "P-2", Event: "e", Adapter: "exec", Outcome: "delivered"}))

	n, err := s.HookFiringCount(ctx, "wake", "P-1", "2000-01-01T00:00:00Z")
	require.NoError(t, err)
	assert.Equal(t, 2, n, "only delivered firings on P-1 count; the error and the other node are excluded")

	future, err := s.HookFiringCount(ctx, "wake", "P-1", "2999-01-01T00:00:00Z")
	require.NoError(t, err)
	assert.Equal(t, 0, future, "nothing at or after a future cutoff")
}

// TestRecordInboxDelivery_Idempotent: a hook inbox delivery is idempotent per
// (agent, event), so re-dispatch of the same event never double-delivers.
func TestRecordInboxDelivery_Idempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	insertJournalEvent(t, s, "evt-1", "comment", `{"to":"dev","body":"go"}`)

	require.NoError(t, s.RecordInboxDelivery(ctx, "dev", 1, "wake"))
	require.NoError(t, s.RecordInboxDelivery(ctx, "dev", 1, "wake"), "re-dispatch is a no-op, not an error")

	var n int
	require.NoError(t, s.ReadDB().QueryRow(
		`SELECT COUNT(*) FROM inbox_deliveries WHERE agent_id = 'dev' AND event_seq = 1`).Scan(&n))
	assert.Equal(t, 1, n, "exactly one delivery row despite two records")

	require.Error(t, s.RecordInboxDelivery(ctx, "", 1, "wake"), "an empty agent id is rejected")
}

// TestAdvanceHookCursor_Monotonic: the scan floor moves forward only; a lower
// seq never rewinds it (FR-20 floor invariant).
func TestAdvanceHookCursor_Monotonic(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	start, err := s.HookCursor(ctx)
	require.NoError(t, err)
	assert.Zero(t, start, "a fresh store's floor is zero")

	require.NoError(t, s.AdvanceHookCursor(ctx, 10))
	require.NoError(t, s.AdvanceHookCursor(ctx, 4)) // lower — must not rewind
	got, err := s.HookCursor(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(10), got, "the floor never rewinds")
}
