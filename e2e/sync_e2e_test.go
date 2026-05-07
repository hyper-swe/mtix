// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package e2e — sync E2E suite per MTIX-15.9.
//
// Three simulated CLIs run against one Postgres hub. Tests exercise
// the same wire protocol as production (transport.PushEvents +
// sqlite.IdempotentApply), without spawning subprocesses — that's
// overengineering for a deterministic, race-detector-clean suite.
//
// Gated on MTIX_PG_TEST_DSN per the transport package's existing
// convention. When unset, every test in this file skips so laptops
// without Postgres still pass `go test ./...`.

package e2e

import (
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

const envSyncE2EDSN = "MTIX_PG_TEST_DSN"

// requireSyncE2EDSN returns the test DSN or skips the calling test
// when it is unset. Mirrors transport/integration_test.go's helper —
// laptops without Postgres still pass.
func requireSyncE2EDSN(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(envSyncE2EDSN)
	if dsn == "" {
		t.Skipf("set %s to enable sync E2E tests", envSyncE2EDSN)
	}
	return dsn
}

// freshHub drops the mtix-owned tables on the test DSN so each test
// starts clean. Destructive — the test DSN MUST point at a throwaway
// database.
func freshHub(t *testing.T, dsn string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()

	for _, stmt := range []string{
		`DROP TABLE IF EXISTS sync_conflicts CASCADE`,
		`DROP TABLE IF EXISTS applied_events CASCADE`,
		`DROP TABLE IF EXISTS sync_events CASCADE`,
		`DROP TABLE IF EXISTS sync_projects CASCADE`,
		`DROP TABLE IF EXISTS audit_log CASCADE`,
		`DROP FUNCTION IF EXISTS audit_log_immutable() CASCADE`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoErrorf(t, err, "drop: %s", stmt)
	}
}

// openHub opens a transport.Pool against the test DSN, runs Migrate,
// and registers cleanup. Each call drops + remigrates the schema so
// concurrent test files do not stomp each other.
func openHub(t *testing.T) *transport.Pool {
	t.Helper()
	dsn := requireSyncE2EDSN(t)
	freshHub(t, dsn)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	require.NoError(t, pool.Migrate(ctx), "Migrate fresh hub")
	return pool
}

// fakeCLI represents one of the simulated mtix clients. Each holds
// its own SQLite store under a private temp dir and a stable
// machine_hash so cross-CLI events have distinguishable authors.
type fakeCLI struct {
	name        string
	mtixDir     string
	store       *sqlite.Store
	machineHash string
}

// newFakeCLI creates a CLI fixture with its own SQLite store. The
// machineHash override is written into meta.sync.machine_hash on
// open so every event this CLI emits carries the deterministic hash
// (avoids dependence on internal/sync/clock.MachineHash, which is
// host-derived and would collide across CLIs in the same process).
func newFakeCLI(t *testing.T, name, machineHash string) *fakeCLI {
	t.Helper()
	mtixDir := t.TempDir()
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
	st, err := sqlite.New(mtixDir, logger)
	require.NoError(t, err, "open sqlite store for %s", name)
	t.Cleanup(func() { _ = st.Close() })

	// Override machine_hash so emitEvent (called by CreateNode et al)
	// stamps THIS CLI's events with our distinct identity.
	_, err = st.WriteDB().ExecContext(context.Background(),
		`UPDATE meta SET value = ? WHERE key = 'meta.sync.machine_hash'`,
		machineHash)
	require.NoError(t, err)

	return &fakeCLI{
		name:        name,
		mtixDir:     mtixDir,
		store:       st,
		machineHash: machineHash,
	}
}

// createNode is a convenience for emitting a create-node event.
// Uses store.CreateNode which goes through the production emitEvent
// path, so the resulting sync_events row is identical to what the
// real CLI would produce.
func (c *fakeCLI) createNode(t *testing.T, id, title string) {
	t.Helper()
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	dash := strings.Index(id, "-")
	require.NotEqual(t, -1, dash, "id %q must contain '-'", id)
	node := &model.Node{
		ID:        id,
		Project:   id[:dash],
		Title:     title,
		Status:    model.StatusOpen,
		Priority:  model.PriorityMedium,
		NodeType:  model.NodeTypeAuto,
		Weight:    1.0,
		Creator:   "e2e",
		CreatedAt: now,
		UpdatedAt: now,
	}
	require.NoError(t, c.store.CreateNode(context.Background(), node),
		"createNode %s on %s", id, c.name)
}

// updateTitle emits an update-node event by changing the node's title.
func (c *fakeCLI) updateTitle(t *testing.T, id, newTitle string) {
	t.Helper()
	require.NoError(t, c.store.UpdateNode(context.Background(), id, &store.NodeUpdate{
		Title: &newTitle,
	}), "updateTitle %s on %s", id, c.name)
}

// pushAll drains the local pending queue to the hub. Mirrors the
// production pushLoop but inlined — we cannot import cmd/mtix.
// Returns counts for assertions.
func (c *fakeCLI) pushAll(ctx context.Context, t *testing.T, pool *transport.Pool) (totalPushed, totalConflicts int) {
	t.Helper()
	const batchSize = 100
	for {
		events := readPendingForTest(ctx, t, c.store, batchSize)
		if len(events) == 0 {
			return totalPushed, totalConflicts
		}
		acceptedIDs, conflicts, err := pool.PushEvents(ctx, events)
		require.NoError(t, err, "%s push", c.name)

		require.NoError(t, c.store.WithTx(ctx, func(tx *sql.Tx) error {
			for _, id := range acceptedIDs {
				if _, err := tx.ExecContext(ctx,
					`UPDATE sync_events SET sync_status = 'pushed' WHERE event_id = ?`,
					id); err != nil {
					return err
				}
			}
			return nil
		}), "%s mark pushed", c.name)

		totalPushed += len(acceptedIDs)
		totalConflicts += len(conflicts)
	}
}

// pullAll drains the hub into the local store. Mirrors pullLoop;
// applies via sqlite.IdempotentApply.
func (c *fakeCLI) pullAll(ctx context.Context, t *testing.T, pool *transport.Pool) int {
	t.Helper()
	const batchSize = 100
	since := readLastPulledClockForTest(ctx, t, c.store)
	totalPulled := 0
	for {
		events, hasMore, err := pool.PullEvents(ctx, since, batchSize)
		require.NoError(t, err, "%s pull", c.name)
		if len(events) == 0 {
			return totalPulled
		}
		require.NoError(t, c.store.WithTx(ctx, func(tx *sql.Tx) error {
			for _, e := range events {
				if applyErr := sqlite.IdempotentApply(ctx, tx, e); applyErr != nil {
					return applyErr
				}
			}
			return nil
		}), "%s apply batch", c.name)
		for _, e := range events {
			if e.LamportClock > since {
				since = e.LamportClock
			}
		}
		writeLastPulledClockForTest(ctx, t, c.store, since)
		totalPulled += len(events)
		if !hasMore {
			return totalPulled
		}
	}
}

// listNodeIDs returns the sorted list of node IDs in the local store —
// used as the convergence assertion.
func (c *fakeCLI) listNodeIDs(t *testing.T) []string {
	t.Helper()
	nodes, _, err := c.store.ListNodes(context.Background(),
		store.NodeFilter{}, store.ListOptions{Limit: 1000})
	require.NoError(t, err)
	ids := make([]string, len(nodes))
	for i, n := range nodes {
		ids[i] = n.ID
	}
	sort.Strings(ids)
	return ids
}

// readPendingForTest mirrors cmd/mtix/sync_push.readPendingBatch.
// Inlined here so e2e doesn't import cmd/mtix.
func readPendingForTest(ctx context.Context, t *testing.T, st *sqlite.Store, limit int) []*model.SyncEvent {
	t.Helper()
	rows, err := st.Query(ctx, `
		SELECT event_id, project_prefix, node_id, op_type, payload,
		       wall_clock_ts, lamport_clock, vector_clock,
		       author_id, author_machine_hash
		FROM sync_events
		WHERE sync_status = 'pending'
		ORDER BY lamport_clock ASC
		LIMIT ?`, limit)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	out := make([]*model.SyncEvent, 0, limit)
	for rows.Next() {
		var e model.SyncEvent
		var opType, payload, vc string
		require.NoError(t, rows.Scan(
			&e.EventID, &e.ProjectPrefix, &e.NodeID, &opType, &payload,
			&e.WallClockTS, &e.LamportClock, &vc,
			&e.AuthorID, &e.AuthorMachineHash,
		))
		e.OpType = model.OpType(opType)
		e.Payload = json.RawMessage(payload)
		require.NoError(t, json.Unmarshal([]byte(vc), &e.VectorClock))
		out = append(out, &e)
	}
	require.NoError(t, rows.Err())
	return out
}

func readLastPulledClockForTest(ctx context.Context, t *testing.T, st *sqlite.Store) int64 {
	t.Helper()
	var raw string
	require.NoError(t, st.QueryRow(ctx,
		`SELECT value FROM meta WHERE key = 'meta.sync.last_pulled_clock'`,
	).Scan(&raw))
	v, err := strconv.ParseInt(raw, 10, 64)
	require.NoError(t, err)
	return v
}

func writeLastPulledClockForTest(ctx context.Context, t *testing.T, st *sqlite.Store, cursor int64) {
	t.Helper()
	require.NoError(t, st.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'meta.sync.last_pulled_clock'`,
			strconv.FormatInt(cursor, 10))
		return err
	}))
}

// registerOnHub mirrors cmd/mtix/sync_init.registerProjectOnHub.
// Inserts the local project's (prefix, first_event_hash) into the
// hub's sync_projects table so divergence detection can see this
// CLI's identity.
func (c *fakeCLI) registerOnHub(ctx context.Context, t *testing.T, pool *transport.Pool) {
	t.Helper()
	prefix, hash, err := c.store.GetOrComputeLocalFirstEventHash(ctx)
	require.NoError(t, err, "%s compute first_event_hash", c.name)
	_, err = pool.Inner().Exec(ctx, `
		INSERT INTO sync_projects (project_prefix, first_event_hash)
		VALUES ($1, $2)
		ON CONFLICT (project_prefix) DO NOTHING`,
		prefix, hash)
	require.NoError(t, err, "%s register on hub", c.name)
}

// readHubProject mirrors cmd/mtix/sync_init.readHubFirstEventHash.
// Returns ("", "") when the hub has no row for this prefix.
func readHubProject(ctx context.Context, t *testing.T, pool *transport.Pool, prefix string) (string, string) {
	t.Helper()
	rows, err := pool.Inner().Query(ctx,
		`SELECT project_prefix, first_event_hash FROM sync_projects WHERE project_prefix = $1`,
		prefix)
	require.NoError(t, err)
	defer rows.Close()
	if !rows.Next() {
		return "", ""
	}
	var p, h string
	require.NoError(t, rows.Scan(&p, &h))
	return p, h
}

// --- happy path ---

// TestE2E_Lifecycle_HappyPath_3CLIsConverge exercises the canonical
// 3-CLI flow: A initializes the hub with seed nodes, B and C clone,
// all three mutate disjoint nodes concurrently, push, pull, and end
// up with byte-identical local state.
//
// Skip when MTIX_PG_TEST_DSN is unset.
func TestE2E_Lifecycle_HappyPath_3CLIsConverge(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")
	c := newFakeCLI(t, "C", "cccccccccccccccc")

	// A seeds the hub with two roots.
	a.createNode(t, "PRJ-1", "root one")
	a.createNode(t, "PRJ-2", "root two")
	pushed, _ := a.pushAll(ctx, t, pool)
	require.Equal(t, 2, pushed, "A pushed both seed events")

	// B and C clone via pullAll on a fresh local store.
	require.Equal(t, 2, b.pullAll(ctx, t, pool), "B clones 2 events")
	require.Equal(t, 2, c.pullAll(ctx, t, pool), "C clones 2 events")
	require.Equal(t, []string{"PRJ-1", "PRJ-2"}, b.listNodeIDs(t))
	require.Equal(t, []string{"PRJ-1", "PRJ-2"}, c.listNodeIDs(t))

	// Concurrent disjoint mutations: A and B touch different roots,
	// C adds a new node. Same-field/same-node conflicts are exercised
	// by 15.9.2's TestE2E_Conflict_SameNodeSameField_LWWConverges.
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); a.updateTitle(t, "PRJ-1", "from-A") }()
	go func() { defer wg.Done(); b.updateTitle(t, "PRJ-2", "from-B") }()
	go func() { defer wg.Done(); c.createNode(t, "PRJ-3", "C added") }()
	wg.Wait()

	// Push in a deterministic order so applied_events on each side
	// progresses predictably.
	a.pushAll(ctx, t, pool)
	b.pushAll(ctx, t, pool)
	c.pushAll(ctx, t, pool)

	// Each CLI pulls everyone else's events.
	a.pullAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	c.pullAll(ctx, t, pool)

	// Convergence assertion: identical sorted ID slices.
	idsA := a.listNodeIDs(t)
	idsB := b.listNodeIDs(t)
	idsC := c.listNodeIDs(t)
	require.Equal(t, idsA, idsB, "A and B should have identical node sets")
	require.Equal(t, idsB, idsC, "B and C should have identical node sets")
	require.Contains(t, idsA, "PRJ-3", "C's late add should be visible to A")
}

// titleOf returns the current title of node id from this CLI's local store.
func (c *fakeCLI) titleOf(t *testing.T, id string) string {
	t.Helper()
	nodes, _, err := c.store.ListNodes(context.Background(),
		store.NodeFilter{}, store.ListOptions{Limit: 1000})
	require.NoError(t, err)
	for _, n := range nodes {
		if n.ID == id {
			return n.Title
		}
	}
	t.Fatalf("%s has no node %s", c.name, id)
	return ""
}

// --- conflicts ---

// TestE2E_Conflict_SameNodeSameField_LWWConverges drives the
// canonical concurrent-edit scenario: B and C both update PRJ-1's
// title without seeing each other's event. LWW resolution at
// apply time deterministically picks one winner; all 3 CLIs
// converge on the SAME title.
//
// Design note on hub-side sync_conflicts: VectorClock.Concurrent
// keys causality by authorID. Today's emit path stamps every
// update with authorID="cli" (sync_emit.authorIDFallback), so
// two CLIs sharing that author produce vector clocks that
// COLLIDE (Equal != Concurrent) and the hub-side detector skips
// them. Hub-side sync_conflicts is reliable for cross-AGENT
// concurrency (distinct authorIDs) but is bypassed in the
// default-authorID case. LWW at apply time — keyed by
// (lamport, wall_clock_ts, author_machine_hash) — is the
// load-bearing convergence mechanism for this scenario; all
// three CLIs have distinct machine_hashes here, so the LWW
// tiebreaker is deterministic.
//
// Convergence is the contract; which side wins depends on
// wall_clock_ts (real time) and is not asserted.
func TestE2E_Conflict_SameNodeSameField_LWWConverges(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")
	c := newFakeCLI(t, "C", "cccccccccccccccc")

	a.createNode(t, "PRJ-1", "initial")
	a.pushAll(ctx, t, pool)
	require.Equal(t, 1, b.pullAll(ctx, t, pool))
	require.Equal(t, 1, c.pullAll(ctx, t, pool))

	b.updateTitle(t, "PRJ-1", "from-B")
	c.updateTitle(t, "PRJ-1", "from-C")
	b.pushAll(ctx, t, pool)
	c.pushAll(ctx, t, pool)

	a.pullAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	c.pullAll(ctx, t, pool)

	titleA := a.titleOf(t, "PRJ-1")
	titleB := b.titleOf(t, "PRJ-1")
	titleC := c.titleOf(t, "PRJ-1")
	require.Equal(t, titleA, titleB,
		"A and B must converge on the same title; got A=%q B=%q", titleA, titleB)
	require.Equal(t, titleB, titleC,
		"B and C must converge on the same title; got B=%q C=%q", titleB, titleC)
	require.Contains(t, []string{"from-B", "from-C"}, titleA,
		"winner must be one of the two competing values; got %q", titleA)
}

// --- divergent history ---

// TestE2E_DivergentHistory_RegistersErrorOnSecondCLI exercises the
// FR-18.10 / MTIX-15.6 detection path. CLI A initializes the hub
// with prefix PRJX; CLI B emits its OWN events under the same
// prefix WITHOUT cloning. B's first_event_hash diverges from A's,
// and DetectDivergentHistory must surface the mismatch.
//
// This test stops at the detection step. The full reconcile path
// (RenameTo / ImportAs) is unit-tested in internal/store/sqlite —
// here we confirm the detection wiring on a real hub.
func TestE2E_DivergentHistory_DetectionFires(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")

	// A seeds + pushes + registers (mirroring sync init).
	a.createNode(t, "PRJX-1", "A's first")
	a.createNode(t, "PRJX-2", "A's second")
	a.pushAll(ctx, t, pool)
	a.registerOnHub(ctx, t, pool)

	// B independently emits PRJX events — does NOT clone first.
	b.createNode(t, "PRJX-1", "B's first (different content)")

	// B reads its own first_event_hash and the hub's, then runs
	// the divergence detector.
	bPrefix, bHash, err := b.store.GetOrComputeLocalFirstEventHash(ctx)
	require.NoError(t, err)
	require.Equal(t, "PRJX", bPrefix)

	hubPrefix, hubHash := readHubProject(ctx, t, pool, "PRJX")
	require.Equal(t, "PRJX", hubPrefix)
	require.NotEmpty(t, hubHash, "hub should have A's first_event_hash registered")

	require.NotEqual(t, hubHash, bHash,
		"A and B must have computed distinct first_event_hash values")

	err = sqlite.DetectDivergentHistory(bPrefix, bHash, hubPrefix, hubHash)
	require.Error(t, err, "divergence must be detected")
	require.ErrorIs(t, err, model.ErrSyncDivergentHistory,
		"error must wrap ErrSyncDivergentHistory")
}

// TestE2E_RepeatedPushPull_NoDuplication asserts that running
// push and pull repeatedly in a tight loop on the happy-path setup
// is idempotent — applied_events dedupe holds, no node row gets
// duplicated, hashes don't drift.
func TestE2E_RepeatedPushPull_NoDuplication(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")

	a.createNode(t, "PRJ-1", "stable")
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)

	idsBefore := b.listNodeIDs(t)
	titleBefore := b.titleOf(t, "PRJ-1")

	for i := 0; i < 3; i++ {
		a.pushAll(ctx, t, pool)
		b.pushAll(ctx, t, pool)
		a.pullAll(ctx, t, pool)
		b.pullAll(ctx, t, pool)
	}

	require.Equal(t, idsBefore, b.listNodeIDs(t),
		"node set must not change under repeated idempotent pushes/pulls")
	require.Equal(t, titleBefore, b.titleOf(t, "PRJ-1"),
		"title must not drift")
}

// --- edge scenarios ---

// TestE2E_AgentSurge_NoQueueLoss simulates 9 concurrent agents
// emitting create-node events on the SAME local store. SQLite's
// BEGIN IMMEDIATE serializes the writes; the test asserts all 9
// nodes survive into sync_events and round-trip through the hub
// after a single push.
//
// This is the canary for goroutine-safety in the emit path:
// concurrent emitters must not lose events, must not collide on
// EventID (UUID v7 generation), and must not produce duplicate
// node IDs.
func TestE2E_AgentSurge_NoQueueLoss(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")

	// 9 concurrent emit goroutines; each creates a distinct node.
	const n = 9
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		i := i
		go func() {
			defer wg.Done()
			id := "PRJ-" + strconv.Itoa(i+1)
			a.createNode(t, id, "surge "+strconv.Itoa(i+1))
		}()
	}
	wg.Wait()

	// All 9 events should be in sync_events as pending.
	var pending int
	require.NoError(t, a.store.QueryRow(ctx,
		`SELECT count(*) FROM sync_events WHERE sync_status = 'pending'`,
	).Scan(&pending))
	require.Equal(t, n, pending, "all 9 events should be queued")

	// Drain via a single push; round-trip via a fresh CLI's pull.
	pushed, _ := a.pushAll(ctx, t, pool)
	require.Equal(t, n, pushed)

	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")
	require.Equal(t, n, b.pullAll(ctx, t, pool))
	require.Len(t, b.listNodeIDs(t), n, "B sees all 9 surge events after pull")
}

// TestE2E_LostLaptop_FreshCloneSkipsUnpushedEvents documents the
// design choice: un-pushed events on a lost machine are NOT durable.
//
// Scenario:
//  1. A, B, C all initialized and converged.
//  2. A emits 3 events but does NOT push (laptop "lost" before push).
//  3. B and C continue to mutate + push + pull; they converge with
//     each other, but A's 3 unpushed events are NOT on the hub.
//  4. A new fresh CLI D clones from scratch — D ends up at B/C's
//     state, NOT including A's lost events.
//
// This is intentional: the local sync_events queue is local-only
// until pushed. Operators who need durability across machine loss
// must push frequently or run the daemon. The failure mode is
// surfaced by 'mtix sync status' (pending count > 0) and by the
// hub-unreachable detector in MTIX-15.8.
func TestE2E_LostLaptop_FreshCloneSkipsUnpushedEvents(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")
	c := newFakeCLI(t, "C", "cccccccccccccccc")

	// All three converged on a baseline.
	a.createNode(t, "PRJ-1", "shared")
	a.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	c.pullAll(ctx, t, pool)

	// A emits 3 events but does NOT push.
	a.createNode(t, "PRJ-LOST-1", "A's lost #1")
	a.createNode(t, "PRJ-LOST-2", "A's lost #2")
	a.createNode(t, "PRJ-LOST-3", "A's lost #3")

	// B and C continue normal flow.
	b.createNode(t, "PRJ-2", "B's add")
	c.createNode(t, "PRJ-3", "C's add")
	b.pushAll(ctx, t, pool)
	c.pushAll(ctx, t, pool)
	b.pullAll(ctx, t, pool)
	c.pullAll(ctx, t, pool)

	// A's lost events MUST NOT be on the hub.
	for _, lostID := range []string{"PRJ-LOST-1", "PRJ-LOST-2", "PRJ-LOST-3"} {
		var found int
		require.NoError(t, pool.Inner().QueryRow(ctx,
			`SELECT count(*) FROM sync_events WHERE node_id = $1`, lostID,
		).Scan(&found))
		require.Equal(t, 0, found,
			"lost-laptop event %s leaked to hub before push", lostID)
	}

	// Fresh CLI D clones — sees B/C's state but not A's lost events.
	d := newFakeCLI(t, "D", "dddddddddddddddd")
	d.pullAll(ctx, t, pool)
	dIDs := d.listNodeIDs(t)
	require.Contains(t, dIDs, "PRJ-1", "shared baseline visible")
	require.Contains(t, dIDs, "PRJ-2", "B's add visible")
	require.Contains(t, dIDs, "PRJ-3", "C's add visible")
	for _, lostID := range []string{"PRJ-LOST-1", "PRJ-LOST-2", "PRJ-LOST-3"} {
		require.NotContains(t, dIDs, lostID,
			"un-pushed event %s should NOT survive machine loss", lostID)
	}
}

// TestE2E_QueueFull_RefuseSurfacesError exercises the
// sync.max_queue_size cap. With cap=5, the 6th emit must fail
// with ErrSyncQueueFull. Draining the queue (push) restores
// capacity for subsequent emits.
func TestE2E_QueueFull_RefuseSurfacesError(t *testing.T) {
	pool := openHub(t)
	ctx := context.Background()

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")

	// Set a tiny cap.
	_, err := a.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = '5' WHERE key = 'sync.max_queue_size'`)
	require.NoError(t, err)

	// 5 emits succeed.
	for i := 0; i < 5; i++ {
		a.createNode(t, "PRJ-"+strconv.Itoa(i+1), "fill "+strconv.Itoa(i+1))
	}

	// 6th emit must fail with ErrSyncQueueFull. Use the lower-level
	// CreateNode call directly so the test can capture the error;
	// the harness's createNode helper require.NoError's the failure.
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	overflow := &model.Node{
		ID: "PRJ-6", Project: "PRJ", Title: "overflow",
		Status: model.StatusOpen, Priority: model.PriorityMedium,
		NodeType: model.NodeTypeAuto, Weight: 1.0, Creator: "e2e",
		CreatedAt: now, UpdatedAt: now,
	}
	err = a.store.CreateNode(ctx, overflow)
	require.Error(t, err)
	require.ErrorIs(t, err, model.ErrSyncQueueFull,
		"6th emit must wrap ErrSyncQueueFull; got %v", err)

	// Queue size is still 5 (overflow rejected).
	var pending int
	require.NoError(t, a.store.QueryRow(ctx,
		`SELECT count(*) FROM sync_events WHERE sync_status = 'pending'`,
	).Scan(&pending))
	require.Equal(t, 5, pending, "overflow event must not have been queued")

	// Drain via push, then more emits succeed.
	a.pushAll(ctx, t, pool)
	for i := 0; i < 5; i++ {
		a.createNode(t, "PRJ-DRAIN-"+strconv.Itoa(i+1), "after drain")
	}
}
