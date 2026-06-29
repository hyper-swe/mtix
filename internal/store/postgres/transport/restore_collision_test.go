// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// --- MTIX-30.8: restore-collision Option B via the hub restore-epoch ---
//
// ADR-003 §6.1 + Addendum A §15. A settled-vs-settled collision is a RESTORE
// collision (Option B) ONLY when the contested number is held by a create
// stamped in an epoch EARLIER than the current restore_epoch — i.e. the two
// creates straddle an operator restore-bump (a cross-epoch re-grant). Every
// same-epoch collision renumbers (§6, MTIX-30.7); Option B is then unreachable.
//
// These tests pin the safety-critical discriminator: the RESTORE scenario, the
// near-misses that MUST NOT trigger Option B, and the same-epoch regression
// guard. The UID-age trigger of the rejected first attempt is gone (§15).

// uidEvent builds a create_node carrying an explicit, distinct uid so the
// registry treats each as a separate logical node (ADR-003 §2).
func uidEvent(id, nodeID, author string, lamport int64) *model.SyncEvent {
	e := makeEvent(id, nodeID, author, lamport)
	e.UID = id
	return e
}

// countOpenCollisions returns the number of open collision rows for a path.
func countOpenCollisions(t *testing.T, pool *transport.Pool, prefix, path string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.Inner().QueryRow(context.Background(),
		`SELECT count(*) FROM sync_node_collisions
		 WHERE project_prefix = $1 AND display_path = $2 AND status = 'open'`,
		prefix, path).Scan(&n))
	return n
}

// stampedEpoch returns the restore_epoch a committed create was stamped with.
func stampedEpoch(t *testing.T, pool *transport.Pool, eventID string) int64 {
	t.Helper()
	var e int64
	require.NoError(t, pool.Inner().QueryRow(context.Background(),
		`SELECT restore_epoch FROM sync_events WHERE event_id = $1`, eventID).Scan(&e))
	return e
}

// TestRestoreEpoch_DefaultsToZeroAndIsOperatorOnly is the un-forgeable gate
// (ADR-003 §15): a fresh hub is at epoch 0, ordinary pushes NEVER advance it,
// and only the operator's MarkRestored bumps it — monotonically.
func TestRestoreEpoch_DefaultsToZeroAndIsOperatorOnly(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	epoch, err := pool.CurrentRestoreEpoch(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), epoch, "fresh hub starts at the no-restore baseline")

	// A normal push must NOT advance the epoch (clients cannot bump it).
	_, _, _, _, err = pool.PushEventsWithCollisions(ctx,
		[]*model.SyncEvent{uidEvent("0193fa00-0000-7000-8000-0000000a0001", "MTIX-1.4", "alice", 1)})
	require.NoError(t, err)
	epoch, err = pool.CurrentRestoreEpoch(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), epoch, "a normal push MUST NOT advance the restore_epoch")

	// Only the operator action advances it, and monotonically.
	got, err := pool.MarkRestored(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), got)
	got, err = pool.MarkRestored(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), got, "MarkRestored is monotonic")

	epoch, err = pool.CurrentRestoreEpoch(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), epoch)
}

// TestRestore_StampsCreatesWithCurrentEpoch proves the hub-side stamp (ADR-003
// §15): a create accepted before any restore is stamped 0; one accepted after
// MarkRestored is stamped with the advanced epoch. The stamp is hub-assigned,
// never client-asserted.
func TestRestore_StampsCreatesWithCurrentEpoch(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	before := uidEvent("0193fa00-0000-7000-8000-0000000a1001", "MTIX-1.4", "alice", 1)
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{before})
	require.NoError(t, err)
	require.Equal(t, int64(0), stampedEpoch(t, pool, before.EventID))

	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)

	after := uidEvent("0193fa00-0000-7000-8000-0000000a1002", "MTIX-1.5", "alice", 2)
	_, _, _, _, err = pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{after})
	require.NoError(t, err)
	require.Equal(t, int64(1), stampedEpoch(t, pool, after.EventID),
		"a create accepted after MarkRestored is stamped with the advanced epoch")
}

// TestRestore_CanceledContext_SurfacesError exercises the error-return paths
// of the collision/epoch surface (cancellation handling + DSN redaction): a
// canceled context must produce a clean error from each query, never a panic.
func TestRestore_CanceledContext_SurfacesError(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // every subsequent query fails fast

	_, err := pool.CurrentRestoreEpoch(ctx)
	require.Error(t, err)
	_, err = pool.MarkRestored(ctx)
	require.Error(t, err)
	_, err = pool.ListOpenCollisions(ctx, "MTIX")
	require.Error(t, err)
	_, err = pool.GetOpenCollision(ctx, 1)
	require.Error(t, err)
	_, err = pool.ResolveCollision(ctx, 1, "w", "MTIX-1.5", "admin")
	require.Error(t, err)
}

// TestRestore_SameEpochCollision_RenumbersNotOptionB is the explicit
// regression guard (ADR-003 §15): with NO restore (epoch 0), a distinct-uid
// settled-vs-settled collision takes the ORDINARY renumber path — it is NEVER
// classified as a restore collision. This is what killed the UID-age attempt.
func TestRestore_SameEpochCollision_RenumbersNotOptionB(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	held := uidEvent("0193fa00-0000-7000-8000-0000000a2001", "MTIX-1.4", "alice", 1)
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{held})
	require.NoError(t, err)

	incoming := uidEvent("0193fa00-0000-7000-8000-0000000a2002", "MTIX-1.4", "bob", 2)
	acc, _, renumbers, collisions, err := pool.PushEventsWithCollisions(ctx,
		[]*model.SyncEvent{incoming})
	require.NoError(t, err)

	require.Empty(t, collisions, "same-epoch collision MUST NOT be a restore collision")
	require.Len(t, renumbers, 1, "same-epoch collision takes the ordinary renumber path")
	require.Equal(t, incoming.EventID, renumbers[0].EventID)
	require.Empty(t, acc)
	require.Equal(t, 0, countOpenCollisions(t, pool, "MTIX", "MTIX-1.4"),
		"no collision row is queued for a same-epoch race")
}

// TestRestore_CrossEpochCollision_TriggersOptionB is the FULL restore scenario
// (ADR-003 §6.1, §15): a settled create (A) survives a restore stamped in an
// earlier epoch; the operator runs MarkRestored; a DIFFERENT node (B) settling
// into the same number is a cross-epoch re-grant → Option B. B is BLOCKED (not
// on the hub) and queued for admin resolution; A is untouched — BOTH nodes
// intact, no node lost.
func TestRestore_CrossEpochCollision_TriggersOptionB(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	// A holds MTIX-1.4, stamped epoch 0 (a pre-restore survivor).
	held := uidEvent("0193fa00-0000-7000-8000-0000000a3001", "MTIX-1.4", "alice", 1)
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{held})
	require.NoError(t, err)

	// Operator advances the epoch (restore-from-backup runbook step).
	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)

	// B (distinct node) settles into MTIX-1.4 in the new epoch → Option B.
	incoming := uidEvent("0193fa00-0000-7000-8000-0000000a3002", "MTIX-1.4", "bob", 2)
	acc, _, renumbers, collisions, err := pool.PushEventsWithCollisions(ctx,
		[]*model.SyncEvent{incoming})
	require.NoError(t, err)

	require.Empty(t, renumbers, "a cross-epoch re-grant is NOT an ordinary renumber")
	require.Empty(t, acc, "the blocked create does not land on the hub")
	require.Len(t, collisions, 1, "exactly one restore collision is surfaced")
	c := collisions[0]
	require.Equal(t, incoming.EventID, c.EventID, "collision names the blocked incoming create")
	require.Equal(t, held.EventID, c.HeldEventID, "collision names the held survivor")
	require.Equal(t, int64(0), c.HeldEpoch, "held was stamped in the earlier epoch")
	require.Equal(t, int64(1), c.DetectedEpoch, "detected in the current (advanced) epoch")
	require.Greater(t, c.DetectedEpoch, c.HeldEpoch, "the two creates straddle the restore boundary")

	// No node lost: A's create is intact on the hub; B is NOT on the hub but
	// is recorded as an open collision for the admin to resolve.
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-1.4"),
		"only the held create remains on the hub (the blocked one is queued, not inserted)")
	require.Equal(t, 1, countOpenCollisions(t, pool, "MTIX", "MTIX-1.4"))
}

// TestRestore_SameEpochAfterRestore_StillRenumbers is a near-miss (ADR-003
// §15): once the restore window closes (both creates accepted in the SAME, now
// current, epoch) a collision renumbers again — the epoch gate is about
// straddling a bump, not merely "epoch > 0".
func TestRestore_SameEpochAfterRestore_StillRenumbers(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	_, err := pool.MarkRestored(ctx) // epoch -> 1 before either create
	require.NoError(t, err)

	held := uidEvent("0193fa00-0000-7000-8000-0000000a4001", "MTIX-2.4", "alice", 1)
	_, _, _, _, err = pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{held})
	require.NoError(t, err)
	require.Equal(t, int64(1), stampedEpoch(t, pool, held.EventID))

	incoming := uidEvent("0193fa00-0000-7000-8000-0000000a4002", "MTIX-2.4", "bob", 2)
	_, _, renumbers, collisions, err := pool.PushEventsWithCollisions(ctx,
		[]*model.SyncEvent{incoming})
	require.NoError(t, err)
	require.Empty(t, collisions, "both creates are in the same (current) epoch — not Option B")
	require.Len(t, renumbers, 1, "same-epoch collision renumbers even after a past restore")
}

// TestRestore_TwoOfflineProvisionals_NoCollision is a near-miss (ADR-003 §6.1):
// two offline provisionals serialize to DISTINCT numbers, so after a restore
// each settles cleanly — no contested number, no Option B.
func TestRestore_TwoOfflineProvisionals_NoCollision(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	_, err := pool.MarkRestored(ctx)
	require.NoError(t, err)

	a := uidEvent("0193fa00-0000-7000-8000-0000000a5001", "MTIX-3.4", "alice", 1)
	b := uidEvent("0193fa00-0000-7000-8000-0000000a5002", "MTIX-3.5", "bob", 2)
	acc, _, renumbers, collisions, err := pool.PushEventsWithCollisions(ctx,
		[]*model.SyncEvent{a, b})
	require.NoError(t, err)
	require.Len(t, acc, 2, "distinct numbers both land")
	require.Empty(t, renumbers)
	require.Empty(t, collisions, "distinct numbers never collide")
}

// TestRestore_SameUIDCrossEpoch_IsNoOp guards the MTIX-30.15 invariant under
// epoch-gating: a re-create with the SAME uid (a --force re-backfill) is the
// same logical node — a no-op — EVEN inside a restore window. Epoch-gating must
// only affect DISTINCT-uid collisions, never the same-node idempotency path.
func TestRestore_SameUIDCrossEpoch_IsNoOp(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	uid := "0193fa00-0000-7000-8000-0000000a6001"
	first := uidEvent(uid, "MTIX-4.4", "alice", 1)
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{first})
	require.NoError(t, err)

	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)

	repush := uidEvent("0193fa00-0000-7000-8000-0000000a6002", "MTIX-4.4", "alice", 2)
	repush.UID = uid // same logical node, fresh event_id
	acc, _, renumbers, collisions, err := pool.PushEventsWithCollisions(ctx,
		[]*model.SyncEvent{repush})
	require.NoError(t, err)
	require.Empty(t, collisions, "same-uid re-create is the same node — never a collision")
	require.Empty(t, renumbers, "same-uid re-create is a no-op, not a renumber")
	require.Equal(t, []string{repush.EventID}, acc, "absorbed no-op is reported accepted")
	require.Equal(t, 0, countOpenCollisions(t, pool, "MTIX", "MTIX-4.4"))
}

// TestRestore_ScopedBlock_OtherEventsLand pins audit F-1: a single restore
// collision BLOCKS only the affected node's create; every other event in the
// same push still lands. One unresolved collision must not wedge the stream.
func TestRestore_ScopedBlock_OtherEventsLand(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	held := uidEvent("0193fa00-0000-7000-8000-0000000a7001", "MTIX-5.4", "alice", 1)
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{held})
	require.NoError(t, err)

	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)

	batch := []*model.SyncEvent{
		uidEvent("0193fa00-0000-7000-8000-0000000a7002", "MTIX-5.5", "bob", 2), // fresh, lands
		uidEvent("0193fa00-0000-7000-8000-0000000a7003", "MTIX-5.4", "bob", 3), // cross-epoch, blocked
		uidEvent("0193fa00-0000-7000-8000-0000000a7004", "MTIX-5.6", "bob", 4), // fresh, lands
	}
	acc, _, renumbers, collisions, err := pool.PushEventsWithCollisions(ctx, batch)
	require.NoError(t, err)

	require.ElementsMatch(t,
		[]string{"0193fa00-0000-7000-8000-0000000a7002", "0193fa00-0000-7000-8000-0000000a7004"},
		acc, "both non-colliding creates land despite the sibling collision")
	require.Empty(t, renumbers)
	require.Len(t, collisions, 1)
	require.Equal(t, "0193fa00-0000-7000-8000-0000000a7003", collisions[0].EventID)

	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-5.5"))
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-5.6"))
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-5.4"), "held survivor only")
}

// TestRestore_IdempotentBlockedRepush_NoDuplicateRow asserts re-pushing the
// SAME blocked create (a flaky-network retry) does not pile up duplicate open
// collision rows — the incoming_event_id unique index makes it idempotent.
func TestRestore_IdempotentBlockedRepush_NoDuplicateRow(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	held := uidEvent("0193fa00-0000-7000-8000-0000000a8001", "MTIX-6.4", "alice", 1)
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{held})
	require.NoError(t, err)
	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)

	incoming := uidEvent("0193fa00-0000-7000-8000-0000000a8002", "MTIX-6.4", "bob", 2)
	for i := 0; i < 3; i++ {
		_, _, _, collisions, pErr := pool.PushEventsWithCollisions(ctx,
			[]*model.SyncEvent{incoming})
		require.NoError(t, pErr)
		require.Len(t, collisions, 1, "each push still reports the collision")
	}
	require.Equal(t, 1, countOpenCollisions(t, pool, "MTIX", "MTIX-6.4"),
		"re-pushing the same blocked create must not duplicate the open row")
}

// TestRestore_ListAndResolveCollision exercises the admin-resolve transport
// surface (ADR-003 §6.1): a recorded collision is listed with BOTH nodes and
// their signals; resolving it flips it closed and records the decision; a
// double-resolve is a no-op. No create event is deleted.
func TestRestore_ListAndResolveCollision(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	held := uidEvent("0193fa00-0000-7000-8000-0000000a9001", "MTIX-7.4", "alice", 1)
	held.WallClockTS = 1000
	_, _, _, _, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{held})
	require.NoError(t, err)
	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)
	incoming := uidEvent("0193fa00-0000-7000-8000-0000000a9002", "MTIX-7.4", "bob", 2)
	incoming.WallClockTS = 2000
	_, _, _, _, err = pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{incoming})
	require.NoError(t, err)

	open, err := pool.ListOpenCollisions(ctx, "MTIX")
	require.NoError(t, err)
	require.Len(t, open, 1)
	c := open[0]
	require.Equal(t, "MTIX-7.4", c.DisplayPath)
	require.Equal(t, held.EventID, c.HeldEventID)
	require.Equal(t, held.UID, c.HeldUID)
	require.Equal(t, int64(1000), c.HeldWallClockTS, "held signal surfaced for the advisory default")
	require.Equal(t, incoming.EventID, c.IncomingEventID)
	require.Equal(t, incoming.UID, c.IncomingUID)
	require.Equal(t, int64(2000), c.IncomingWallClockTS)
	require.Equal(t, int64(0), c.HeldEpoch)
	require.Equal(t, int64(1), c.DetectedEpoch)

	// Admin picks the held node as winner; the incoming (loser) renumbers
	// elsewhere (its new path is recorded for the audit trail).
	ok, err := pool.ResolveCollision(ctx, c.CollisionID, held.EventID, "MTIX-7.5", "admin")
	require.NoError(t, err)
	require.True(t, ok, "an open collision resolves exactly once")

	// Closed: no longer listed, and GetOpenCollision returns the zero value.
	open, err = pool.ListOpenCollisions(ctx, "MTIX")
	require.NoError(t, err)
	require.Empty(t, open)
	got, err := pool.GetOpenCollision(ctx, c.CollisionID)
	require.NoError(t, err)
	require.Equal(t, int64(0), got.CollisionID, "resolved collision is not open")

	// Double-resolve is a no-op, not a clobber.
	ok, err = pool.ResolveCollision(ctx, c.CollisionID, held.EventID, "MTIX-7.5", "admin")
	require.NoError(t, err)
	require.False(t, ok, "a resolved collision cannot be resolved again")

	// No create event is ever deleted: the held create is still on the hub.
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-7.4"))
}
