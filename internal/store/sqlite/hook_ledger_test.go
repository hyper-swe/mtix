// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const testLease = time.Minute

// backdateClaim rewrites a ledger row's fired_at so it looks older than the
// claim lease — the in-test equivalent of a trigger that claimed and then
// crashed before firing (MTIX-56.1 §7).
func backdateClaim(t *testing.T, s *sqlite.Store, hook string, seq int64, age time.Duration) {
	t.Helper()
	stale := time.Now().Add(-age).UTC().Format(time.RFC3339)
	_, err := s.WriteDB().Exec(
		`UPDATE hook_dispatch_ledger SET fired_at = ? WHERE hook_name = ? AND event_seq = ?`,
		stale, hook, seq)
	require.NoError(t, err)
}

// TestHookLedger_ClaimExactlyOnce: the PK on (hook_name, event_seq) makes the
// first claim win and every later claim of the same pair lose — the
// exactly-once primitive of FR-20 §4.2.
func TestHookLedger_ClaimExactlyOnce(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	won, err := s.ClaimHookDispatch(ctx, "wake", 1, testLease)
	require.NoError(t, err)
	require.True(t, won, "first claim must win")

	again, err := s.ClaimHookDispatch(ctx, "wake", 1, testLease)
	require.NoError(t, err)
	require.False(t, again, "a second claim of the same (hook,event) must lose")

	other, err := s.ClaimHookDispatch(ctx, "wake", 2, testLease)
	require.NoError(t, err)
	require.True(t, other, "a different event is an independent claim")

	otherHook, err := s.ClaimHookDispatch(ctx, "notify", 1, testLease)
	require.NoError(t, err)
	require.True(t, otherHook, "a different hook is an independent claim")
}

// TestHookLedger_ConcurrentClaims_OneWinner: concurrent triggers (daemon tick,
// on-commit, a second process) race on the PK; exactly one wins (FR-20 §7).
func TestHookLedger_ConcurrentClaims_OneWinner(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	const racers = 8
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		wins int
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			won, err := s.ClaimHookDispatch(ctx, "wake", 7, testLease)
			require.NoError(t, err)
			if won {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Equal(t, 1, wins, "exactly one racer may win the claim")
}

// TestHookLedger_ReclaimStaleClaim: a 'claimed' row older than the lease is a
// crashed trigger; re-claiming it wins so the wake is never lost
// (at-least-once, FR-20 §7). Fresh claims and terminal outcomes never reclaim.
func TestHookLedger_ReclaimStaleClaim(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	won, err := s.ClaimHookDispatch(ctx, "wake", 1, testLease)
	require.NoError(t, err)
	require.True(t, won)

	fresh, err := s.ClaimHookDispatch(ctx, "wake", 1, testLease)
	require.NoError(t, err)
	require.False(t, fresh, "a fresh claim is owned; no reclaim")

	backdateClaim(t, s, "wake", 1, 2*testLease)
	reclaimed, err := s.ClaimHookDispatch(ctx, "wake", 1, testLease)
	require.NoError(t, err)
	require.True(t, reclaimed, "a stale claim (crash before fire) must be reclaimable")

	// A terminal outcome is final: never reclaimed, no matter how old.
	require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", 1, "delivered"))
	backdateClaim(t, s, "wake", 1, 2*testLease)
	after, err := s.ClaimHookDispatch(ctx, "wake", 1, testLease)
	require.NoError(t, err)
	require.False(t, after, "a delivered row must never re-fire")
}

// TestHookLedger_ErrorOutcomeIsTerminal: outcome=error means the fire RAN and
// failed — never auto-retried (§14.3: a re-fired broken wake script is a loop
// generator). Only crash-before-fire (stale 'claimed') re-fires.
func TestHookLedger_ErrorOutcomeIsTerminal(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	won, err := s.ClaimHookDispatch(ctx, "wake", 3, testLease)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", 3, "error"))

	backdateClaim(t, s, "wake", 3, 2*testLease)
	again, err := s.ClaimHookDispatch(ctx, "wake", 3, testLease)
	require.NoError(t, err)
	require.False(t, again, "an errored fire is terminal; no auto-retry")

	stale, err := s.StaleHookClaims(ctx, testLease)
	require.NoError(t, err)
	require.Empty(t, stale, "terminal rows are not stale claims")
}

// TestHookLedger_StaleHookClaims lists only claimed-and-expired rows — the
// belt-and-braces reclaim scan input (FR-20 §7), floor-independent.
func TestHookLedger_StaleHookClaims(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for seq := int64(1); seq <= 3; seq++ {
		won, err := s.ClaimHookDispatch(ctx, "wake", seq, testLease)
		require.NoError(t, err)
		require.True(t, won)
	}
	require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", 2, "delivered"))
	backdateClaim(t, s, "wake", 1, 2*testLease) // stale claim (crashed)
	backdateClaim(t, s, "wake", 2, 2*testLease) // old but delivered
	// seq 3 stays a FRESH claim (in flight)

	stale, err := s.StaleHookClaims(ctx, testLease)
	require.NoError(t, err)
	require.Equal(t, []sqlite.HookClaim{{Hook: "wake", Seq: 1}}, stale)
}

// TestHookScanFloor_ClampedBelowClaims: the floor never advances past a
// 'claimed' row — a crashed trigger's event must stay inside the scan window
// (or the reclaim re-fire would be skipped and pruned; FR-20 §12).
func TestHookScanFloor_ClampedBelowClaims(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	won, err := s.ClaimHookDispatch(ctx, "wake", 5, testLease)
	require.NoError(t, err)
	require.True(t, won)

	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, 10))
	floor, err := s.HookCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(4), floor, "floor clamps below the open claim at 5")

	// Once the claim resolves, the next advance passes it.
	require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", 5, "delivered"))
	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, 10))
	floor, err = s.HookCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(10), floor)

	// Monotonic: a lower target never rewinds it.
	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, 3))
	floor, err = s.HookCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(10), floor)
}

// TestHookLedger_Prune: compaction deletes terminal rows at/below the floor,
// keeps rows above it, and NEVER deletes a 'claimed' row (its re-fire evidence).
func TestHookLedger_Prune(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	for seq := int64(1); seq <= 4; seq++ {
		won, err := s.ClaimHookDispatch(ctx, "wake", seq, testLease)
		require.NoError(t, err)
		require.True(t, won)
	}
	require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", 1, "delivered"))
	require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", 3, "error"))
	// seq 2 stays 'claimed' (crashed trigger); seq 4 is terminal above the floor.
	require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", 4, "delivered"))

	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, 3)) // clamps to 1
	require.NoError(t, s.PruneHookDispatchLedger(ctx))

	var remaining []int64
	rows, err := s.ReadDB().Query(`SELECT event_seq FROM hook_dispatch_ledger ORDER BY event_seq`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var seq int64
		require.NoError(t, rows.Scan(&seq))
		remaining = append(remaining, seq)
	}
	require.NoError(t, rows.Err())
	// Floor clamped to 1 → the delivered row at 1 is pruned; claimed row at 2
	// survives (never pruned); 3 and 4 are above the floor.
	require.Equal(t, []int64{2, 3, 4}, remaining)
}

// TestHookLedger_FreshClaimRefusedAtOrBelowFloor: a seq at/below the scan
// floor is terminally dispatched (that is the only way the floor passes it)
// and its ledger row may be pruned — a FRESH claim for it must lose, or a
// trigger holding a stale floor snapshot re-fires a pruned pair (the prune vs
// concurrent-claim race). A stale 'claimed' ROW below the floor must still be
// reclaimable — that is the crash-recovery path, keyed on the row existing.
func TestHookLedger_FreshClaimRefusedAtOrBelowFloor(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	// Simulate racer A's completed pass: delivered at seq 3, floor advanced,
	// ledger pruned.
	won, err := s.ClaimHookDispatch(ctx, "wake", 3, testLease)
	require.NoError(t, err)
	require.True(t, won)
	require.NoError(t, s.RecordHookDispatchOutcome(ctx, "wake", 3, "delivered"))
	require.NoError(t, s.AdvanceHookScanFloorClamped(ctx, 3))
	require.NoError(t, s.PruneHookDispatchLedger(ctx))

	// Racer B, floor snapshot from before the advance, tries to claim seq 3.
	again, err := s.ClaimHookDispatch(ctx, "wake", 3, testLease)
	require.NoError(t, err)
	require.False(t, again, "a fresh claim at/below the floor must lose — the pair was dispatched and pruned")

	// But a stale claimed ROW below the floor is still reclaimable (crash path).
	won, err = s.ClaimHookDispatch(ctx, "wake", 2, testLease)
	require.NoError(t, err)
	require.False(t, won, "no fresh claim below the floor")
	_, err = s.WriteDB().Exec(`
		INSERT INTO hook_dispatch_ledger (hook_name, event_seq, fired_at, outcome)
		VALUES ('wake', 2, '2026-01-01T00:00:00Z', 'claimed')`)
	require.NoError(t, err) // the stranded row a crashed trigger left behind
	reclaimed, err := s.ClaimHookDispatch(ctx, "wake", 2, testLease)
	require.NoError(t, err)
	require.True(t, reclaimed, "a stale claimed row is reclaimable even below the floor")
}

// insertJournalEvent writes a raw sync_events row so ledger tests can exercise
// journal reads without driving the full service mutation path.
func insertJournalEvent(t *testing.T, s *sqlite.Store, eventID, opType, payload string) {
	t.Helper()
	_, err := s.WriteDB().Exec(`
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash, sync_status, created_at)
		VALUES (?, 'PROJ', 'PROJ-1', ?, ?,
		        1, 1, '{}', 'alice', '0123456789abcdef',
		        'pending', '2026-01-01T00:00:00Z')`, eventID, opType, payload)
	require.NoError(t, err)
}

// TestJournalTail reports the highest sync_events rowid (0 when empty) — the
// bootstrap floor-init input (FR-20 §8).
func TestJournalTail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	tail, err := s.JournalTail(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), tail)

	insertJournalEvent(t, s, "e1", "create_node", `{}`)
	insertJournalEvent(t, s, "e2", "comment", `{"to":"opus"}`)
	tail, err = s.JournalTail(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), tail)
}

// TestInitHookScanFloorAtTail: the bootstrap call jumps the floor to the tail
// so pulled/cloned history is never a hook backlog (FR-20 §8); it is monotonic
// and safe on an empty store.
func TestInitHookScanFloorAtTail(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	require.NoError(t, s.InitHookScanFloorAtTail(ctx)) // empty store: floor stays 0
	floor, err := s.HookCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), floor)

	insertJournalEvent(t, s, "e1", "create_node", `{}`)
	insertJournalEvent(t, s, "e2", "transition_status", `{"to":"done"}`)
	require.NoError(t, s.InitHookScanFloorAtTail(ctx))
	floor, err = s.HookCursor(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), floor, "bootstrap floor = journal tail: history arrives pre-dispatched")
}

// TestReadJournalEventAt: single-event fetch feeding the stale-claim re-fire —
// exact hit, miss past the tail, and no off-by-one bleed to a neighbor.
func TestReadJournalEventAt(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	insertJournalEvent(t, s, "e1", "create_node", `{}`)
	insertJournalEvent(t, s, "e2", "comment", `{"to":"opus"}`)

	je, ok, err := s.ReadJournalEventAt(ctx, 2)
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "e2", je.EventID)
	require.Equal(t, "comment", je.OpType)

	_, ok, err = s.ReadJournalEventAt(ctx, 3)
	require.NoError(t, err)
	require.False(t, ok, "no event past the tail")
}
