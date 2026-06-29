// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	syncpkg "github.com/hyper-swe/mtix/internal/sync"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// migratedPool opens a fresh test pool and runs Migrate so every test
// here starts from the full hub schema (sync_events + the Phase 1 remap
// ledger). Skips when MTIX_PG_TEST_DSN is unset.
func migratedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))
	return pool.Inner()
}

// poolFor wraps an already-migrated *pgxpool.Pool back into a
// transport.Pool via the test seam so the sweep/index methods under test
// run against the same DB the test fabricates rows on.
func poolFor(t *testing.T, _ *pgxpool.Pool) *transport.Pool {
	t.Helper()
	dsn := requireTestDSN(t)
	p, err := transport.New(context.Background(), dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(p.Close)
	return p
}

// migratedPoolWithoutIndex applies the full hub schema EXCEPT the partial
// unique index (009), reproducing a hub mid-migration: Phase 1 has not yet
// run, so the index is intentionally absent and EnsureRegistryIndex is the
// thing that adds it. It opens fresh (drops first) so each test is isolated.
func migratedPoolWithoutIndex(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := requireTestDSN(t)
	freshSchema(t, dsn)
	ctx := context.Background()

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	db, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	t.Cleanup(db.Close)

	files, err := migrations.Files()
	require.NoError(t, err)
	for _, name := range files {
		if name == "009_node_registry_index.sql" {
			continue
		}
		body, rerr := migrations.Read(name)
		require.NoError(t, rerr)
		_, eerr := db.Exec(ctx, body)
		require.NoErrorf(t, eerr, "exec %s", name)
	}
	// Ensure the index really is absent.
	_, _ = db.Exec(ctx, `DROP INDEX IF EXISTS sync_events_node_registry_uidx`)
	return db
}

// insertCreate inserts a create_node row DIRECTLY (bypassing the registry
// guard) so a test can fabricate the pre-existing duplicates that exist on
// projects bitten by MTIX-28 before the partial unique index landed.
func insertCreate(t *testing.T, db *pgxpool.Pool, eventID, prefix, nodeID, uid string, lamport int64) {
	t.Helper()
	var uidArg any
	if uid == "" {
		uidArg = nil
	} else {
		uidArg = uid
	}
	_, err := db.Exec(context.Background(), `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, uid, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash)
		VALUES ($1, $2, $3, $4, 'create_node', '{"title":"x"}',
		        $5, $6, '{"alice":1}', 'alice', '0123456789abcdef')`,
		eventID, prefix, nodeID, uidArg, time.Now().UnixMilli(), lamport,
	)
	require.NoError(t, err)
}

func countRemaps(t *testing.T, db *pgxpool.Pool, prefix string) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(context.Background(),
		`SELECT count(*) FROM node_renumber_remaps WHERE project_prefix = $1`, prefix,
	).Scan(&n))
	return n
}

func countConflicts(t *testing.T, db *pgxpool.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, db.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_conflicts WHERE field_name = 'display_path'`,
	).Scan(&n))
	return n
}

// --- Phase 1: dedup sweep ---

// CORNER: a clean project (no duplicate numbers) ⇒ the sweep is a NO-OP.
func TestSweep_CleanProjectIsNoOp(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	insertCreate(t, db, "e1", "MTIX", "MTIX-1", "e1", 1)
	insertCreate(t, db, "e2", "MTIX", "MTIX-2", "e2", 2)

	rep, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 0, rep.Resolved, "clean project sweep resolves nothing")
	require.Equal(t, 0, countRemaps(t, db, "MTIX"))
	require.Equal(t, 0, countConflicts(t, db))
}

// HAPPY/CORNER: a project with a pre-existing duplicate number resolves
// deterministically (lowest event_id wins) and records a loud remap +
// conflict for the loser.
func TestSweep_DuplicateResolvesDeterministically(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	// Two DISTINCT logical nodes both claimed MTIX-1.4. e1 < e2 lexically
	// ⇒ e1 wins, e2 is the loser that must renumber.
	insertCreate(t, db, "e1", "MTIX", "MTIX-1.4", "e1", 1)
	insertCreate(t, db, "e2", "MTIX", "MTIX-1.4", "e2", 2)

	rep, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 1, rep.Resolved)
	require.Equal(t, 1, countRemaps(t, db, "MTIX"))
	require.Equal(t, 1, countConflicts(t, db))

	// The recorded remap names the loser uid (e2) and the winner (e1).
	var loserUID, winner, oldPath string
	require.NoError(t, db.QueryRow(context.Background(), `
		SELECT uid, winner_event_id, old_display_path
		FROM node_renumber_remaps WHERE project_prefix = 'MTIX'`,
	).Scan(&loserUID, &winner, &oldPath))
	require.Equal(t, "e2", loserUID, "lowest event_id wins ⇒ e2 is the loser")
	require.Equal(t, "e1", winner)
	require.Equal(t, "MTIX-1.4", oldPath)
}

// EDGE: three creates for the SAME number ⇒ one winner, TWO losers.
func TestSweep_TripleDuplicate(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	insertCreate(t, db, "a1", "MTIX", "MTIX-7", "a1", 1)
	insertCreate(t, db, "a2", "MTIX", "MTIX-7", "a2", 2)
	insertCreate(t, db, "a3", "MTIX", "MTIX-7", "a3", 3)

	rep, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 2, rep.Resolved, "one winner, two losers")
	require.Equal(t, 2, countRemaps(t, db, "MTIX"))
}

// CORNER: a SAME-logical-node duplicate (same uid, e.g. a --force
// re-backfill that re-minted an event_id) is NOT a collision and must
// NOT be renumbered (MTIX-30.15 false-collision class).
func TestSweep_SameUIDIsNotRenumbered(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	// Both rows carry uid=u1 ⇒ the SAME node, re-minted. Not a collision.
	insertCreate(t, db, "e1", "MTIX", "MTIX-1", "u1", 1)
	insertCreate(t, db, "e2", "MTIX", "MTIX-1", "u1", 2)

	rep, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 0, rep.Resolved, "same uid ⇒ same node ⇒ no renumber")
	require.Equal(t, 0, countRemaps(t, db, "MTIX"))
}

// EDGE/crash-resume: running the sweep twice is IDEMPOTENT — the second
// run resolves nothing new and records no duplicate remap.
func TestSweep_IdempotentReRun(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	insertCreate(t, db, "e1", "MTIX", "MTIX-1.4", "e1", 1)
	insertCreate(t, db, "e2", "MTIX", "MTIX-1.4", "e2", 2)

	rep1, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 1, rep1.Resolved)

	rep2, err := pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 0, rep2.Resolved, "re-run is a no-op; the loser is already remapped")
	require.Equal(t, 1, countRemaps(t, db, "MTIX"), "no double-renumber")
	require.Equal(t, 1, countConflicts(t, db))
}

// EDGE: two concurrent sweeps serialize via the single-flight advisory
// lock — exactly one resolves the duplicate, the other observes the
// already-clean state.
func TestSweep_ConcurrentSingleFlight(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	insertCreate(t, db, "e1", "MTIX", "MTIX-9", "e1", 1)
	insertCreate(t, db, "e2", "MTIX", "MTIX-9", "e2", 2)

	type result struct {
		resolved int
		err      error
	}
	results := make(chan result, 2)
	for i := 0; i < 2; i++ {
		go func() {
			rep, err := pool.SweepDuplicates(context.Background(), "MTIX")
			r := result{err: err}
			if err == nil {
				r.resolved = rep.Resolved
			}
			results <- r
		}()
	}

	total := 0
	for i := 0; i < 2; i++ {
		r := <-results
		require.NoError(t, r.err)
		total += r.resolved
	}
	require.Equal(t, 1, total, "exactly one sweep resolves the duplicate")
	require.Equal(t, 1, countRemaps(t, db, "MTIX"))
}

// Empty-prefix guard reached on an OPEN pool (covers the validation
// branch that the nil-pool unit test cannot reach).
func TestSweepDuplicates_OpenPoolEmptyPrefixErrors(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)
	_, err := pool.SweepDuplicates(context.Background(), "")
	require.Error(t, err)
	_, err = pool.PreviewDuplicates(context.Background(), "")
	require.Error(t, err)
	_, err = pool.EnsureRegistryIndex(context.Background(), "")
	require.Error(t, err)
}

// PreviewDuplicates counts losers without recording anything (dry-run).
func TestPreviewDuplicates_CountsWithoutMutating(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	insertCreate(t, db, "e1", "MTIX", "MTIX-1.4", "e1", 1)
	insertCreate(t, db, "e2", "MTIX", "MTIX-1.4", "e2", 2)

	n, err := pool.PreviewDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, 0, countRemaps(t, db, "MTIX"), "preview must not record any remap")

	// After applying, the preview reports 0 pending (already resolved).
	_, err = pool.SweepDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	n, err = pool.PreviewDuplicates(context.Background(), "MTIX")
	require.NoError(t, err)
	require.Equal(t, 0, n, "already-resolved losers are not pending")
}

// --- Phase 1.5: version-gated index add ---

// ERROR/ordering: attempting Phase 1.5 (the index) on a DIRTY log (before
// Phase 1) must error loudly, not silently corrupt.
func TestEnsureRegistryIndex_DirtyLogErrors(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	// Register a single up-to-date client so the version gate is OPEN.
	require.NoError(t, pool.UpsertProjectClient(context.Background(),
		"MTIX", "aaaaaaaaaaaaaaaa", syncpkg.UIDKeyedMinVersion))

	insertCreate(t, db, "e1", "MTIX", "MTIX-1.4", "e1", 1)
	insertCreate(t, db, "e2", "MTIX", "MTIX-1.4", "e2", 2)

	_, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.Error(t, err, "adding the unique index to a log with duplicates must fail loudly")
}

// GATE-CLOSED: a project with a stale (below-min) client defers the index.
func TestEnsureRegistryIndex_DeferredWhileGateClosed(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	insertCreate(t, db, "e1", "MTIX", "MTIX-1", "e1", 1)
	require.NoError(t, pool.UpsertProjectClient(context.Background(),
		"MTIX", "aaaaaaaaaaaaaaaa", "0.1.0")) // below UIDKeyedMinVersion

	res, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.NoError(t, err)
	require.False(t, res.Added, "index deferred while the version gate is closed")
	require.False(t, res.GateOpen)
}

// GATE-OPEN happy path: a clean log + all-compatible clients ⇒ index added.
func TestEnsureRegistryIndex_AddedWhenCleanAndGateOpen(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	insertCreate(t, db, "e1", "MTIX", "MTIX-1", "e1", 1)
	require.NoError(t, pool.UpsertProjectClient(context.Background(),
		"MTIX", "aaaaaaaaaaaaaaaa", syncpkg.UIDKeyedMinVersion))

	res, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.NoError(t, err)
	require.True(t, res.GateOpen)
	require.True(t, res.Added)

	// The index now exists and rejects a second distinct create.
	insertCreate(t, db, "e2", "MTIX", "MTIX-2", "e2", 2)
	_, err = db.Exec(context.Background(), `
		INSERT INTO sync_events
		  (event_id, project_prefix, node_id, op_type, payload,
		   wall_clock_ts, lamport_clock, vector_clock,
		   author_id, author_machine_hash)
		VALUES ('e3','MTIX','MTIX-1','create_node','{}',1,3,'{}','a','0123456789abcdef')`)
	require.Error(t, err, "the partial unique index must reject a duplicate create")
}

// IDEMPOTENT: a second EnsureRegistryIndex when the index already exists
// reports Added=false without error.
func TestEnsureRegistryIndex_IdempotentWhenAlreadyPresent(t *testing.T) {
	db := migratedPoolWithoutIndex(t)
	pool := poolFor(t, db)

	insertCreate(t, db, "e1", "MTIX", "MTIX-1", "e1", 1)
	require.NoError(t, pool.UpsertProjectClient(context.Background(),
		"MTIX", "aaaaaaaaaaaaaaaa", syncpkg.UIDKeyedMinVersion))

	res1, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.NoError(t, err)
	require.True(t, res1.Added)

	res2, err := pool.EnsureRegistryIndex(context.Background(), "MTIX")
	require.NoError(t, err)
	require.False(t, res2.Added, "index already present ⇒ no re-add")
	require.True(t, res2.GateOpen)
}

var _ = model.OpCreateNode
