// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Crash-resilience / chaos coverage for the distributed-identity write paths
// (MTIX-30.11, ADR-003 §5/§6/§7). An operation interrupted mid-flight must
// leave a CONSISTENT, RESUMABLE state, and a re-run must CONVERGE — never a
// torn subtree, a half-recorded sweep, or a duplicated/lost node.
//
// These drive the REAL store and the REAL transport (no mocks), exercising the
// transactional guarantees end to end:
//
//   - RenumberSubtree (ADR-003 §5, audit F-2): an atomic subtree rename. A
//     rejected/interrupted renumber changes NOTHING; concurrent renumbers and
//     reads never observe a torn subtree; a re-run is idempotent. [no hub]
//   - SweepDuplicates (ADR-003 §7 Phase 1): the migration dedup sweep. An
//     interrupted sweep records no partial state and strands no advisory lock;
//     a re-run is idempotent; concurrent sweeps single-flight (each loser
//     resolved exactly once); a clean project is a no-op. [PG hub]
//   - RenumberForHubRejection (ADR-003 §6, MTIX-30.7): the push renumber-drain.
//     A crash AFTER the local renumber but BEFORE the re-push resumes and
//     converges; re-driving the drain is idempotent (no duplicate, no loss).
//     [PG hub]
//
// COVERAGE BOUND (logged, not silent): a literal kill -9 of these multi-step
// flows lands at a random instruction; the higher-value property for them is
// resumability BETWEEN logical steps, exercised here by interrupting at each
// step and re-running. Literal SIGKILL crash-recovery of the underlying SQLite
// write path (WAL atomicity) is covered by e2e/faultinject (the create and
// sync-backfill kill-9 suites); a binary-driven SIGKILL of `mtix sync
// push`/`migrate` against a live hub is DEFERRED there (it needs both a fault
// volume and a hub, and adds no guarantee beyond WAL atomicity + the
// step-level resumability proven here).
//
// The RenumberSubtree test needs no hub and always runs. The sweep and
// push-drain tests are gated on MTIX_PG_TEST_DSN (openHub); they skip when it
// is unset, like the rest of the sync e2e suite.

package e2e

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// --- 1. RenumberSubtree: atomic, untorn, idempotent (ADR-003 §5/F-2) -------

// TestE2E_Chaos_RenumberSubtree_AtomicNoTornSubtree drives the REAL sqlite
// store: a renumber that fails (or is interrupted) must leave the subtree
// byte-identical (atomic rollback), concurrent renumbers must never tear it,
// and a completed renumber must move the WHOLE subtree consistently and be
// idempotent on re-run. No hub required.
func TestE2E_Chaos_RenumberSubtree_AtomicNoTornSubtree(t *testing.T) {
	ctx := context.Background()
	c := newFakeCLI(t, "renum", "1111111111111111")

	// A deep subtree under PRJX-1.1, plus PRJX-1.2 occupying a target number.
	c.createNode(t, "PRJX-1", "root")
	c.createNode(t, "PRJX-1.1", "branch")
	c.createNode(t, "PRJX-1.1.1", "child")
	c.createNode(t, "PRJX-1.1.1.1", "grandchild")
	c.createNode(t, "PRJX-1.2", "occupied sibling")
	assertNodesConsistent(ctx, t, c)
	before := snapshotIDs(ctx, t, c)

	// (a) Atomic rollback: renumber PRJX-1.1 -> 2 (taken by PRJX-1.2). The
	// pre-flight namespace guard rejects it; NOTHING in the subtree moves.
	err := c.store.RenumberSubtree(ctx, "PRJX-1.1", 2)
	require.Error(t, err, "renumber onto an occupied number must fail")
	require.Equal(t, before, snapshotIDs(ctx, t, c),
		"a rejected renumber must leave the subtree byte-identical (no torn write)")
	assertNodesConsistent(ctx, t, c)

	// (b) Interrupted mid-flight via a cancelled context: the transaction must
	// roll back, again leaving the subtree untouched. This drives the real
	// cancellation path through WithTx.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	err = c.store.RenumberSubtree(cancelled, "PRJX-1.1", 7)
	require.Error(t, err, "a renumber on a cancelled context must not commit")
	require.Equal(t, before, snapshotIDs(ctx, t, c),
		"an interrupted renumber must leave no partial state")
	assertNodesConsistent(ctx, t, c)

	// (c) Re-run convergence + idempotency: settle PRJX-1.1 onto a known-free
	// number, verify the whole subtree moved together, then renumber again to
	// the SAME number — a clean no-op.
	require.NoError(t, c.store.RenumberSubtree(ctx, "PRJX-1.1", 5))
	moved := snapshotIDs(ctx, t, c)
	for _, want := range []string{"PRJX-1.5", "PRJX-1.5.1", "PRJX-1.5.1.1"} {
		assert.Contains(t, moved, want, "the whole subtree moved to the new root")
	}
	for _, gone := range []string{"PRJX-1.1", "PRJX-1.1.1", "PRJX-1.1.1.1"} {
		assert.NotContains(t, moved, gone, "no descendant was left behind (untorn)")
	}
	require.NoError(t, c.store.RenumberSubtree(ctx, "PRJX-1.5", 5),
		"renumber to the current number is an idempotent no-op")
	require.Equal(t, moved, snapshotIDs(ctx, t, c), "the idempotent re-run changed nothing")
	assertNodesConsistent(ctx, t, c)

	// (d) Concurrent renumber chaos (LAST, since it relocates the root
	// unpredictably): hammer the subtree with competing renumbers and reads.
	// Each writer re-resolves the branch by its STABLE uid (ADR-003 §2) so a
	// moved root is still targeted. WAL snapshot isolation (ADR-003 §5) means
	// every read observes all-old or all-new — never torn. After the storm the
	// forest is still well-formed.
	branchUID := uidOf(ctx, t, c, "PRJX-1.5")
	require.NotEmpty(t, branchUID)
	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		tornBy []error // any inconsistency a concurrent reader observed
	)
	for i := 0; i < 8; i++ {
		seq := 6 + i // distinct free targets
		wg.Add(1)
		go func() {
			defer wg.Done()
			// Re-resolve the (possibly moved) branch by its stable uid; "" means
			// a racing renumber is mid-flight, just skip this pass. Best-effort:
			// some win, some race onto a taken number (ErrAlreadyExists) or a
			// stale id (not-found). The store must never TEAR regardless.
			if id := currentID(ctx, c, branchUID); id != "" {
				_ = c.store.RenumberSubtree(ctx, id, seq)
			}
		}()
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := checkNodesConsistent(ctx, c); err != nil { // read during writes
				mu.Lock()
				tornBy = append(tornBy, err)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	require.Empty(t, tornBy, "a concurrent reader observed a torn subtree mid-renumber")
	assertNodesConsistent(ctx, t, c)
	require.NotEmpty(t, currentID(ctx, c, branchUID),
		"the branch survived the storm under its stable uid (no node lost)")
}

// --- 2. SweepDuplicates: interrupted, idempotent, single-flight (§7 P1) -----

// TestE2E_Chaos_SweepDuplicates_IdempotentSingleFlight drives the REAL
// transport against a real hub. It seeds a log that already contains duplicate
// (project, display_path) create events (the pre-index MTIX-28 state), then
// asserts: an interrupted sweep records NO partial state, a re-run converges
// and is idempotent, concurrent sweeps single-flight (each loser resolved
// exactly once via the advisory lock), and a clean project is a no-op.
func TestE2E_Chaos_SweepDuplicates_IdempotentSingleFlight(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	// Reconstruct the pre-index log: drop the partial unique index so duplicate
	// (project, display_path) creates can coexist (the MTIX-28 state). Then seed
	// two duplicate groups directly into the log, bypassing the push registry
	// (which would otherwise renumber the second create). Winner is the lowest
	// event_id; each group has exactly one loser ⇒ 2 losers.
	dropRegistryIndex(ctx, t, pool)
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-1.1", "evt-0001-win", "uid-A")
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-1.1", "evt-0002-lose", "uid-B")
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-1.2", "evt-0003-win", "uid-C")
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-1.2", "evt-0004-lose", "uid-D")
	const wantLosers = 2

	// (a) Interrupted sweep: a cancelled context must abort with no partial
	// state and no stranded advisory lock (the xact lock auto-releases on
	// rollback). If the lock leaked, the real sweep in (b) would block forever.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	_, err := pool.SweepDuplicates(cancelled, "PRJX")
	require.Error(t, err, "a sweep on a cancelled context must abort")
	require.Equal(t, 0, countRemaps(ctx, t, pool),
		"an interrupted sweep must record NO partial remaps")

	// (b) Real sweep: converges, resolving every loser. That it resolves the
	// FULL count proves the interrupted sweep consumed nothing. Bound it so a
	// stranded advisory lock surfaces as a clear timeout, not a suite hang.
	bounded, cancelB := context.WithTimeout(ctx, 20*time.Second)
	defer cancelB()
	rep, err := pool.SweepDuplicates(bounded, "PRJX")
	require.NoError(t, err, "the sweep must not block (no stranded lock)")
	require.Equal(t, wantLosers, rep.Resolved, "every duplicate loser is resolved")
	require.Len(t, rep.Remaps, wantLosers)
	require.Equal(t, wantLosers, countRemaps(ctx, t, pool))

	// (c) Idempotent re-run: the uid-keyed remap ledger makes a second sweep a
	// no-op (resolves nothing new, no double-renumber).
	rep2, err := pool.SweepDuplicates(ctx, "PRJX")
	require.NoError(t, err)
	require.Equal(t, 0, rep2.Resolved, "a re-run of a swept project resolves nothing")
	require.Equal(t, wantLosers, countRemaps(ctx, t, pool), "no loser was renumbered twice")

	// (d) Single-flight: many concurrent sweeps of a freshly-dirtied project
	// must, in aggregate, resolve each loser EXACTLY once — the pg advisory
	// lock serializes them so none double-renumbers.
	freshDirtyHub(ctx, t, pool)
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-2.1", "evt-1001-win", "uid-E")
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-2.1", "evt-1002-lose", "uid-F")
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-2.2", "evt-1003-win", "uid-G")
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-2.2", "evt-1004-lose", "uid-H")

	const racers = 6
	var (
		wg          sync.WaitGroup
		mu          sync.Mutex
		totalSolved int
	)
	for i := 0; i < racers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, sErr := pool.SweepDuplicates(ctx, "PRJX")
			assert.NoError(t, sErr)
			mu.Lock()
			totalSolved += r.Resolved
			mu.Unlock()
		}()
	}
	wg.Wait()
	require.Equal(t, wantLosers, totalSolved,
		"concurrent sweeps single-flight: each loser resolved exactly once across all racers")
	require.Equal(t, wantLosers, countRemaps(ctx, t, pool), "no double-renumber under contention")

	// (e) Clean project: a sweep with no duplicates is a no-op.
	freshDirtyHub(ctx, t, pool)
	seedRawCreate(ctx, t, pool, "PRJX", "PRJX-3.1", "evt-2001", "uid-clean")
	clean, err := pool.SweepDuplicates(ctx, "PRJX")
	require.NoError(t, err)
	require.Equal(t, 0, clean.Resolved, "a clean project sweep resolves nothing")
}

// --- 3. Push renumber-drain: resumable, idempotent (ADR-003 §6) ------------

// TestE2E_Chaos_PushRenumberDrain_ResumableConverges simulates a process crash
// in the MIDDLE of the push renumber-drain: the local renumber
// (RenumberForHubRejection) is applied but the process dies BEFORE the re-push.
// On restart the drain resumes and converges — both nodes survive at distinct
// numbers — and re-driving the whole push is idempotent (no duplicate, no loss).
func TestE2E_Chaos_PushRenumberDrain_ResumableConverges(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "a", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "b", "bbbbbbbbbbbbbbbb")
	seedSharedParent(ctx, t, pool, a, b)

	const (
		aTitle = "A: fix login"
		bTitle = "B: add export"
	)
	// A wins PRJX-1.1.
	a.createNode(t, "PRJX-1.1", aTitle)
	a.pushAll(ctx, t, pool)

	// B mints the same number; advance B's local counter as the real create
	// path would so the drain claims the NEXT free seq (not a self no-op).
	_, err := b.store.ClaimNextSeq(ctx, "PRJX", "PRJX-1")
	require.NoError(t, err)
	b.createNode(t, "PRJX-1.1", bTitle)

	// B's first push gets a renumber-required outcome.
	_, _, renB, colB := b.pushOnce(ctx, t, pool)
	require.Empty(t, colB)
	require.Len(t, renB, 1, "B's colliding create is renumber-required")

	// --- CRASH POINT: apply the local renumber, then "die" before re-push.
	newPath, err := b.store.RenumberForHubRejection(ctx, renB[0].EventID)
	require.NoError(t, err)
	require.Equal(t, "PRJX-1.2", newPath, "B renumbers off the contested number locally")
	assertNodesConsistent(ctx, t, b)

	// The renumbered create is re-queued pending and now carries the new path;
	// the hub has NOT yet seen it (the crash was before the re-push).
	require.Equal(t, "PRJX-1.2", pendingCreatePath(ctx, t, b, renB[0].EventID),
		"the pending create is re-stamped with the new number, ready to resume")
	require.Zero(t, hubCreateCount(ctx, t, pool, "PRJX-1.2"),
		"nothing reached the hub before the crash")

	// --- RESTART: resume the drain. It pushes the renumbered create cleanly.
	b.pushAll(ctx, t, pool)
	a.pullAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)

	require.Equal(t, []string{"PRJX-1", "PRJX-1.1", "PRJX-1.2"}, a.listNodeIDs(t),
		"both tickets survive at distinct numbers after resume")
	assertConverged(t, a, b)
	require.Equal(t, "PRJX-1.1", titleByContent(t, a, aTitle))
	require.Equal(t, "PRJX-1.2", titleByContent(t, a, bTitle))

	// --- Idempotent re-drive: running the full push/pull cycle again changes
	// nothing — no duplicate node, no second renumber.
	idsBefore := a.listNodeIDs(t)
	for i := 0; i < 2; i++ {
		a.pushAll(ctx, t, pool)
		b.pushAll(ctx, t, pool)
		a.pullAll(ctx, t, pool)
		b.pullAll(ctx, t, pool)
	}
	require.Equal(t, idsBefore, a.listNodeIDs(t), "re-driving the drain is idempotent")
	assertConverged(t, a, b)
	require.Equal(t, 1, hubCreateCount(ctx, t, pool, "PRJX-1.2"),
		"exactly one create_node holds the renumbered number on the hub")
}

// --- helpers ---------------------------------------------------------------

// snapshotIDs returns the sorted set of live node ids as a set, for byte-exact
// before/after comparison of a (non-)mutating operation.
func snapshotIDs(ctx context.Context, t *testing.T, c *fakeCLI) map[string]bool {
	t.Helper()
	rows, err := c.store.Query(ctx, `SELECT id FROM nodes WHERE deleted_at IS NULL`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		require.NoError(t, rows.Scan(&id))
		out[id] = true
	}
	require.NoError(t, rows.Err())
	return out
}

// checkNodesConsistent verifies the nodes table is a well-formed forest: no id
// is duplicated and every non-root node's parent_id references a live node. A
// torn renumber (some descendants moved, others not) would orphan a node or
// dangle a parent_id; this returns a non-nil error describing it (ADR-003
// §5/F-2). It returns an error rather than failing the test so it is safe to
// call from a goroutine (testify require must not run off the test goroutine).
func checkNodesConsistent(ctx context.Context, c *fakeCLI) error {
	rows, err := c.store.Query(ctx,
		`SELECT id, parent_id FROM nodes WHERE deleted_at IS NULL`)
	if err != nil {
		return fmt.Errorf("query nodes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	ids := map[string]bool{}
	type link struct{ id, parent string }
	var links []link
	for rows.Next() {
		var id string
		var parent sql.NullString
		if scanErr := rows.Scan(&id, &parent); scanErr != nil {
			return fmt.Errorf("scan node: %w", scanErr)
		}
		if ids[id] {
			return fmt.Errorf("duplicate node id %q (torn renumber)", id)
		}
		ids[id] = true
		if parent.Valid && parent.String != "" {
			links = append(links, link{id, parent.String})
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("iterate nodes: %w", rowsErr)
	}
	for _, l := range links {
		if !ids[l.parent] {
			return fmt.Errorf("node %q has dangling parent_id %q (torn/orphaned subtree)", l.id, l.parent)
		}
	}
	return nil
}

// assertNodesConsistent fails the test if the nodes table is not a well-formed
// forest. Call only from the test goroutine.
func assertNodesConsistent(ctx context.Context, t *testing.T, c *fakeCLI) {
	t.Helper()
	require.NoError(t, checkNodesConsistent(ctx, c))
}

// uidOf returns the stable uid (create-event id, ADR-003 §2) of the live node
// currently at id.
func uidOf(ctx context.Context, t *testing.T, c *fakeCLI, id string) string {
	t.Helper()
	var uid sql.NullString
	require.NoError(t, c.store.QueryRow(ctx,
		`SELECT uid FROM nodes WHERE id = ? AND deleted_at IS NULL`, id).Scan(&uid))
	return uid.String
}

// currentID returns the live node's current display id for a stable uid, or ""
// when none is currently visible or on any read error. It NEVER fails the test,
// so it is safe to call from a goroutine during the concurrent renumber storm
// (a renumber may be mid-flight; the WAL snapshot just hasn't advanced here).
func currentID(ctx context.Context, c *fakeCLI, uid string) string {
	var id string
	if err := c.store.QueryRow(ctx,
		`SELECT id FROM nodes WHERE uid = ? AND deleted_at IS NULL`, uid).Scan(&id); err != nil {
		return ""
	}
	return id
}

// dropRegistryIndex removes the partial unique index (migration 009) so a test
// can reconstruct the pre-index log state the Phase 1 sweep is designed to
// clean: duplicate (project, display_path) creates cannot exist while the index
// is present, but a hub migrated by a pre-009 CLI has no such index (ADR-003 §7
// Phase 1 / Phase 1.5 adds it only AFTER the sweep).
func dropRegistryIndex(ctx context.Context, t *testing.T, pool *transport.Pool) {
	t.Helper()
	_, err := pool.Inner().Exec(ctx, `DROP INDEX IF EXISTS sync_events_node_registry_uidx`)
	require.NoError(t, err)
}

// seedRawCreate inserts a create_node row straight into the hub log, bypassing
// the push registry — used to reconstruct a pre-index log that already holds
// duplicate (project, display_path) creates (the MTIX-28 state the Phase 1
// sweep must clean). restore_epoch / created_at take their column defaults.
func seedRawCreate(ctx context.Context, t *testing.T, pool *transport.Pool, prefix, displayPath, eventID, uid string) {
	t.Helper()
	_, err := pool.Inner().Exec(ctx, `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, uid, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash)
		VALUES ($1, $2, $3, $4, 'create_node', $5, $6, $7, $8, 'seed', 'seedmachine')`,
		eventID, prefix, displayPath, uid,
		[]byte(fmt.Sprintf(`{"title":%q}`, displayPath)),
		int64(1), int64(1), []byte(`{}`),
	)
	require.NoError(t, err, "seed raw create %s", eventID)
}

// countRemaps returns the number of rows in the uid-keyed remap ledger — the
// durable record of every loser the sweep renumbered.
func countRemaps(ctx context.Context, t *testing.T, pool *transport.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM node_renumber_remaps`).Scan(&n))
	return n
}

// hubCreateCount returns how many create_node rows on the hub carry displayPath.
func hubCreateCount(ctx context.Context, t *testing.T, pool *transport.Pool, displayPath string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM sync_events WHERE node_id = $1 AND op_type = 'create_node'`,
		displayPath).Scan(&n))
	return n
}

// pendingCreatePath returns the node_id (display_path) the local pending
// create_node with the given event_id currently carries.
func pendingCreatePath(ctx context.Context, t *testing.T, c *fakeCLI, eventID string) string {
	t.Helper()
	var path string
	require.NoError(t, c.store.QueryRow(ctx,
		`SELECT node_id FROM sync_events WHERE event_id = ? AND op_type = 'create_node'`,
		eventID).Scan(&path))
	return path
}

// withTimeout runs fn under a bounded context so a stranded advisory lock
// surfaces as a timeout failure instead of hanging the suite.
func withTimeout[T any](ctx context.Context, d time.Duration, fn func(context.Context) (T, error)) (T, error) {
	tctx, cancel := context.WithTimeout(ctx, d)
	defer cancel()
	return fn(tctx)
}

// freshDirtyHub resets the hub and reconstructs the pre-index log state the
// Phase 1 sweep is designed for: drop + remigrate the schema, then drop the
// partial unique index so duplicate creates can be seeded (ADR-003 §7 Phase 1).
func freshDirtyHub(ctx context.Context, t *testing.T, pool *transport.Pool) {
	t.Helper()
	freshHub(t, requireSyncE2EDSN(t))
	require.NoError(t, pool.Migrate(ctx))
	dropRegistryIndex(ctx, t, pool)
}
