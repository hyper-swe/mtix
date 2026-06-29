// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// End-to-end coverage of the restore-collision discriminator (MTIX-30.11,
// ADR-003 §6.1 + Addendum A §15, scenario matrix §12 sc.7/sc.8) against a
// REAL Postgres hub.
//
// The one decision under test: a settled-vs-settled number collision is
// classified as a RESTORE collision (→ Option B: block + human resolve) ONLY
// when the held create was hub-stamped in an epoch STRICTLY EARLIER than the
// current restore_epoch — i.e. the two creates straddle an operator
// restore-bump (`mtix sync mark-restored`). Every SAME-epoch collision — and
// every collision on a hub that was never restored — takes the ordinary
// renumber path (ADR-003 §6, MTIX-30.7); Option B is unreachable, so a
// compromised client cannot manufacture it (§15).
//
// These assert the discriminator end-to-end: the POSITIVE cross-epoch case
// DOES block (Option B, no node lost, block-scope per F-1, admin resolves),
// and the three §12 sc.8 near-misses (same-epoch race; two offline
// provisionals; no-restore-ever) do NOT.
//
// Gated on MTIX_PG_TEST_DSN (openHub); skips when unset, like the rest of the
// sync e2e suite.

package e2e

import (
	"context"
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// pushOnce drains the local pending queue to the hub in a SINGLE pass via the
// full collision-aware transport surface (PushEventsWithCollisions) and
// returns every structured outcome WITHOUT auto-resolving renumbers or
// collisions. Accepted events are marked pushed; a renumbered/blocked create
// stays pending. Unlike pushAll (which drains renumber-required outcomes), this
// lets a test assert EXACTLY which outcome the registry produced — renumber vs
// restore-collision — which is the whole point of the discriminator (ADR-003
// §6/§6.1/§15).
func (c *fakeCLI) pushOnce(ctx context.Context, t *testing.T, pool *transport.Pool) (
	accepted []string, conflicts []transport.ConflictDescriptor,
	renumbers []transport.RenumberRequired, collisions []transport.RestoreCollision,
) {
	t.Helper()
	events := readPendingForTest(ctx, t, c.store, 100)
	if len(events) == 0 {
		return nil, nil, nil, nil
	}
	accepted, conflicts, renumbers, collisions, err := pool.PushEventsWithCollisions(ctx, events)
	require.NoError(t, err, "%s pushOnce", c.name)

	require.NoError(t, c.store.WithTx(ctx, func(tx *sql.Tx) error {
		for _, id := range accepted {
			if _, execErr := tx.ExecContext(ctx,
				`UPDATE sync_events SET sync_status = 'pushed' WHERE event_id = ?`,
				id); execErr != nil {
				return execErr
			}
		}
		return nil
	}), "%s mark pushed", c.name)
	return accepted, conflicts, renumbers, collisions
}

// seedSharedParent seeds PRJX-1 from a and clones it into every other CLI — the
// realistic shared starting point every restore scenario builds on.
func seedSharedParent(ctx context.Context, t *testing.T, pool *transport.Pool, a *fakeCLI, others ...*fakeCLI) {
	t.Helper()
	a.createNode(t, "PRJX-1", "shared parent epic")
	a.pushAll(ctx, t, pool)
	for _, o := range others {
		require.Equal(t, 1, o.pullAll(ctx, t, pool), "%s clones the parent", o.name)
	}
}

// TestE2E_Restore_CrossEpoch_TriggersOptionB is the positive case — ADR-003
// §6.1 worked example / §12 sc.7. A settles a clean number; the operator then
// restores the hub and bumps the epoch (mark-restored); a DIFFERENT node
// (distinct uid) belonging to the earlier era pushes the SAME number. Because
// the held create's stamp predates the bump, the collision is a cross-epoch
// re-grant: it is BLOCKED for admin resolution (Option B), NOT silently
// renumbered. No node is lost; the admin resolves; both survive at distinct
// numbers.
func TestE2E_Restore_CrossEpoch_TriggersOptionB(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "a", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "b", "bbbbbbbbbbbbbbbb")
	seedSharedParent(ctx, t, pool, a, b)

	// A settles PRJX-1.1; its create lands stamped at the baseline epoch 0.
	const (
		aTitle = "A's ticket (keeps the number)"
		bTitle = "B's distinct ticket (earlier era)"
	)
	a.createNode(t, "PRJX-1.1", aTitle)
	acc, _, ren, col := a.pushOnce(ctx, t, pool)
	require.Len(t, acc, 1, "A's create lands as first-writer")
	require.Empty(t, ren)
	require.Empty(t, col)

	require.EqualValues(t, 0, mustEpoch(ctx, t, pool), "baseline epoch is 0 (no-restore)")

	// Operator restores the hub from backup and advances the epoch — the
	// un-forgeable gate (ADR-003 §15). This is the ONLY way the Option B path
	// opens; no client can do it.
	newEpoch, err := pool.MarkRestored(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, newEpoch, "mark-restored advances the epoch to 1")

	// B (a distinct logical node from the earlier era) pushes the SAME number.
	// held(A, epoch 0) < current(1) ⇒ cross-epoch ⇒ Option B, NOT a renumber.
	// Advance B's local sibling counter first, as the real create path does
	// (ClaimNextSeq, ADR-003 §4) — the createNode helper writes an explicit id
	// and skips that claim, so without this the later RenumberForHubRejection
	// would re-claim the still-free seq 1 (a no-op) instead of the next number.
	_, err = b.store.ClaimNextSeq(ctx, "PRJX", "PRJX-1")
	require.NoError(t, err)
	b.createNode(t, "PRJX-1.1", bTitle)
	accB, _, renB, colB := b.pushOnce(ctx, t, pool)
	require.Empty(t, accB, "the blocked create is NOT inserted on the hub")
	require.Empty(t, renB, "a cross-epoch collision must NOT auto-renumber (it is Option B)")
	require.Len(t, colB, 1, "a cross-epoch distinct-uid collision is a RESTORE collision")
	require.Equal(t, "PRJX-1.1", colB[0].DisplayPath)
	require.Less(t, colB[0].HeldEpoch, colB[0].DetectedEpoch,
		"the cross-epoch fingerprint: held stamp strictly precedes the detection epoch")

	// It is durably queued for the operator (ADR-003 §6.1, audit F-1).
	open, err := pool.ListOpenCollisions(ctx, "PRJX")
	require.NoError(t, err)
	require.Len(t, open, 1, "the collision is recorded for admin resolution")
	oc := open[0]
	require.Equal(t, "PRJX-1.1", oc.DisplayPath)

	// --- Admin resolution (Option B, human-gated): A keeps the number, B
	// renumbers. This is exactly what `mtix sync collisions resolve` does
	// (cmd/mtix/sync_collisions.go): renumber the loser locally via the
	// ordinary RenumberForHubRejection primitive, then record the decision.
	loserNewPath, err := b.store.RenumberForHubRejection(ctx, oc.IncomingUID)
	require.NoError(t, err, "loser renumbers off the contested number (no node lost)")
	require.NotEqual(t, "PRJX-1.1", loserNewPath, "the loser moved off PRJX-1.1")

	ok, err := pool.ResolveCollision(ctx, oc.CollisionID, oc.HeldEventID, loserNewPath, "admin@test")
	require.NoError(t, err)
	require.True(t, ok, "the open collision flips to resolved exactly once")

	// B re-pushes the renumbered create — now a free number, no further block.
	_, _, renB2, colB2 := b.pushOnce(ctx, t, pool)
	require.Empty(t, colB2, "the renumbered create no longer collides")
	require.Empty(t, renB2)

	// Everyone converges; BOTH tickets survive at DISTINCT numbers.
	a.pullAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	assertConverged(t, a, b)
	require.Equal(t, "PRJX-1.1", titleByContent(t, a, aTitle), "A keeps PRJX-1.1")
	require.Equal(t, loserNewPath, titleByContent(t, a, bTitle), "B settled into the freed number")

	openAfter, err := pool.ListOpenCollisions(ctx, "PRJX")
	require.NoError(t, err)
	require.Empty(t, openAfter, "no open collisions remain after resolution")
}

// TestE2E_Restore_CrossEpoch_BlockScope_OneNodeOnly asserts audit finding F-1:
// a single Option B collision blocks ONLY the affected create — every other
// event in the same push still lands. One unresolved collision must never wedge
// the team's sync stream (ADR-003 §6.1).
func TestE2E_Restore_CrossEpoch_BlockScope_OneNodeOnly(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "a", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "b", "bbbbbbbbbbbbbbbb")
	seedSharedParent(ctx, t, pool, a, b)

	a.createNode(t, "PRJX-1.1", "A holds 1.1")
	a.pushAll(ctx, t, pool)
	_, err := pool.MarkRestored(ctx)
	require.NoError(t, err)

	// B pushes a BATCH: a colliding create (PRJX-1.1) AND an unrelated, free
	// create (PRJX-1.2). Only the collider is withheld; the bystander lands.
	b.createNode(t, "PRJX-1.1", "B's collider (earlier era)")
	b.createNode(t, "PRJX-1.2", "B's unrelated bystander")
	acc, _, _, col := b.pushOnce(ctx, t, pool)
	require.Len(t, col, 1, "exactly the collider is blocked")
	require.Equal(t, "PRJX-1.1", col[0].DisplayPath)
	require.Len(t, acc, 1, "the unrelated event still lands (block scope, F-1)")

	// The bystander is visible to A on pull; the team stream is not wedged.
	a.pullAll(ctx, t, pool)
	assert.Contains(t, allTitles(t, a), "B's unrelated bystander",
		"a blocked node must not stop other nodes from syncing")
}

// TestE2E_Restore_NearMiss_NoRestoreEver_Renumbers — ADR-003 §12 sc.8
// "no-restore-ever" / §15. On a hub the operator never restored (epoch 0), the
// Option B path is structurally closed: even an N-way concurrent-create
// collision resolves entirely by renumber, and NOTHING is ever queued in
// sync_node_collisions.
func TestE2E_Restore_NearMiss_NoRestoreEver_Renumbers(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "a", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "b", "bbbbbbbbbbbbbbbb")
	c := newFakeCLI(t, "c", "cccccccccccccccc")
	seedSharedParent(ctx, t, pool, a, b, c)

	require.EqualValues(t, 0, mustEpoch(ctx, t, pool), "the hub was never restored")

	// Three distinct nodes race for PRJX-1.1.
	a.createNode(t, "PRJX-1.1", "A ticket")
	b.createNode(t, "PRJX-1.1", "B ticket")
	c.createNode(t, "PRJX-1.1", "C ticket")

	// A wins; B and C each get a same-epoch renumber, never a collision.
	_, _, renA, colA := a.pushOnce(ctx, t, pool)
	require.Empty(t, renA)
	require.Empty(t, colA)
	for _, cli := range []*fakeCLI{b, c} {
		_, _, ren, col := cli.pushOnce(ctx, t, pool)
		require.Empty(t, col, "%s: epoch 0 ⇒ Option B is unreachable", cli.name)
		require.Len(t, ren, 1, "%s: a same-epoch collision is an ordinary renumber", cli.name)
	}

	open, err := pool.ListOpenCollisions(ctx, "PRJX")
	require.NoError(t, err)
	require.Empty(t, open, "no restore ⇒ nothing is ever queued for Option B")

	// And it self-heals: drain the renumbers and converge to 3 distinct numbers.
	for _, cli := range []*fakeCLI{b, c} {
		cli.pushAll(ctx, t, pool)
	}
	a.pullAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	c.pullAll(ctx, t, pool)
	assertConverged(t, a, b, c)
	require.Equal(t, []string{"PRJX-1", "PRJX-1.1", "PRJX-1.2", "PRJX-1.3"}, a.listNodeIDs(t),
		"all three concurrent creates survive at distinct numbers")
}

// TestE2E_Restore_NearMiss_SameEpochWithinWindow_Renumbers — ADR-003 §12 sc.8
// "same-epoch race" / §15. The precise complement of the positive test: even
// INSIDE a restore window (epoch already advanced to 1), two creates BOTH
// stamped in that SAME current epoch collide → renumber, NOT Option B. Being in
// an elevated epoch is not the trigger; a cross-epoch STRADDLE is.
func TestE2E_Restore_NearMiss_SameEpochWithinWindow_Renumbers(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "a", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "b", "bbbbbbbbbbbbbbbb")
	seedSharedParent(ctx, t, pool, a, b)

	// Open a restore window FIRST, then create + push both colliders, so BOTH
	// land stamped at the current epoch (1) — a same-epoch contest.
	_, err := pool.MarkRestored(ctx)
	require.NoError(t, err)
	require.EqualValues(t, 1, mustEpoch(ctx, t, pool))

	a.createNode(t, "PRJX-1.1", "A within-window")
	b.createNode(t, "PRJX-1.1", "B within-window")

	_, _, renA, colA := a.pushOnce(ctx, t, pool)
	require.Empty(t, renA)
	require.Empty(t, colA, "A is first-writer at the current epoch")

	_, _, renB, colB := b.pushOnce(ctx, t, pool)
	require.Empty(t, colB, "held.epoch == current ⇒ same-epoch ⇒ NO Option B")
	require.Len(t, renB, 1, "an in-window same-epoch collision still renumbers")

	open, err := pool.ListOpenCollisions(ctx, "PRJX")
	require.NoError(t, err)
	require.Empty(t, open)
}

// TestE2E_Restore_NearMiss_TwoOfflineProvisionals_Renumbers — ADR-003 §12 sc.8
// "two offline provisionals + restore". Two nodes created while offline have
// settled NOTHING before the restore; when they reconnect AFTER the operator
// bump, both settle into the current epoch, so their collision serializes to
// distinct numbers by ordinary renumber — neither held a number across the
// restore boundary, so Option B never fires.
func TestE2E_Restore_NearMiss_TwoOfflineProvisionals_Renumbers(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "a", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "b", "bbbbbbbbbbbbbbbb")
	seedSharedParent(ctx, t, pool, a, b)

	// Both create offline (nothing pushed yet) — neither has settled a number.
	a.createNode(t, "PRJX-1.1", "A offline create")
	b.createNode(t, "PRJX-1.1", "B offline create")

	// The hub is restored while both are still offline; the operator bumps.
	_, err := pool.MarkRestored(ctx)
	require.NoError(t, err)

	// They reconnect and push: whoever lands first settles at the current
	// epoch; the other collides same-epoch ⇒ renumber, not Option B.
	a.pushAll(ctx, t, pool)
	_, _, renB, colB := b.pushOnce(ctx, t, pool)
	require.Empty(t, colB, "two post-restore settles are same-epoch ⇒ NO Option B")
	require.Len(t, renB, 1, "the later offline provisional renumbers")

	open, err := pool.ListOpenCollisions(ctx, "PRJX")
	require.NoError(t, err)
	require.Empty(t, open)

	// Self-heal to distinct numbers, fully converged.
	b.pushAll(ctx, t, pool)
	a.pullAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	assertConverged(t, a, b)
	require.Equal(t, []string{"PRJX-1", "PRJX-1.1", "PRJX-1.2"}, a.listNodeIDs(t))
}

// mustEpoch reads the hub's current restore_epoch, failing the test on error.
func mustEpoch(ctx context.Context, t *testing.T, pool *transport.Pool) int64 {
	t.Helper()
	epoch, err := pool.CurrentRestoreEpoch(ctx)
	require.NoError(t, err)
	return epoch
}
