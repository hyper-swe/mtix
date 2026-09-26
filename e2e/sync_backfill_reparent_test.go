// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// End-to-end coverage for the reparented-child backfill bug (MTIX-88) and
// the remediation gaps it exposed (MTIX-89), against a real Postgres hub.
//
// Derived from a reproduction recipe supplied by the reporter, so the shape
// matches a real deployment rather than a synthetic case: a child whose
// created_at predates its eventual parent, plus a second project prefix,
// because their hub carries five.
//
// Gated on MTIX_PG_TEST_DSN like the rest of e2e; laptops without Postgres
// skip. freshHub is destructive — the DSN must be a throwaway.

// reparentUnder moves child under parent directly in SQLite, the way
// historical triage does over time: the child row already exists and keeps
// its original created_at, so it ends up OLDER than its new parent. There
// is no CLI reparent command, which is exactly why the condition is easy
// to reach and hard to notice.
func reparentUnder(t *testing.T, c *fakeCLI, child, parent string, depth int) {
	t.Helper()
	_, err := c.store.WriteDB().ExecContext(context.Background(),
		`UPDATE nodes SET parent_id = ?, depth = ? WHERE id = ?`, parent, depth, child)
	require.NoError(t, err, "reparent %s under %s", child, parent)
}

// createEventOrder returns each node's create_node lamport, in emit order.
func createEventOrder(t *testing.T, c *fakeCLI) map[string]int64 {
	t.Helper()
	rows, err := c.store.WriteDB().QueryContext(context.Background(),
		`SELECT node_id, lamport_clock FROM sync_events
		  WHERE op_type = 'create_node' ORDER BY lamport_clock ASC`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	order := map[string]int64{}
	for rows.Next() {
		var id string
		var lam int64
		require.NoError(t, rows.Scan(&id, &lam))
		if _, seen := order[id]; !seen { // keep the FIRST occurrence
			order[id] = lam
		}
	}
	require.NoError(t, rows.Err())
	return order
}

// TestE2E_Backfill_ReparentedChild_ClonesWithoutFKError is the MTIX-88
// regression at full scale: backfill → push → clone through a real hub.
//
// Before the fix the child was emitted with a LOWER lamport than its
// parent, and because nodes.parent_id is a FOREIGN KEY the clone failed
// the child insert with "FOREIGN KEY constraint failed" every time.
func TestE2E_Backfill_ReparentedChild_ClonesWithoutFKError(t *testing.T) {
	ctx := context.Background()
	pool := openHub(t)

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")

	// The child is created FIRST, so its created_at predates the parent's.
	a.createNode(t, "PRJX-1", "Orphan child ticket")
	a.createNode(t, "PRJX-2", "New parent epic")
	reparentUnder(t, a, "PRJX-1", "PRJX-2", 1)

	a.registerOnHub(ctx, t, pool)
	a.wipeLocalSyncEvents(t) // the pre-backfill upgrader state

	_, err := a.store.Backfill(ctx, false)
	require.NoError(t, err, "backfill")

	order := createEventOrder(t, a)
	require.Contains(t, order, "PRJX-1")
	require.Contains(t, order, "PRJX-2")
	require.Lessf(t, order["PRJX-2"], order["PRJX-1"],
		"parent PRJX-2 (lamport %d) must be emitted before its reparented child PRJX-1 (lamport %d)",
		order["PRJX-2"], order["PRJX-1"])

	pushed, conflicts := a.pushAll(ctx, t, pool)
	require.Positive(t, pushed, "events must reach the hub")
	require.Zero(t, conflicts, "a fresh hub must take the backfill cleanly")

	// A second client clones. This is where the bug surfaced: applying in
	// lamport order inserted the child before its parent.
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")
	pulled := b.pullAll(ctx, t, pool)
	require.Positive(t, pulled, "clone must apply events")

	require.ElementsMatch(t, []string{"PRJX-1", "PRJX-2"}, b.listNodeIDs(t),
		"clone must materialize both nodes")

	var parentID string
	require.NoError(t, b.store.WriteDB().QueryRowContext(ctx,
		`SELECT COALESCE(parent_id, '') FROM nodes WHERE id = 'PRJX-1'`).Scan(&parentID))
	require.Equal(t, "PRJX-2", parentID, "the reparented hierarchy must survive the clone")
}

// TestE2E_Backfill_ForceAppendsRatherThanRegenerating pins the MTIX-89 gap.
//
// This is a CHARACTERIZATION test: it documents what --force does TODAY,
// which is not what an operator recovering from a bad stream needs. When
// MTIX-89 lands a regenerate path, this test must be updated deliberately
// rather than silently — that is the point of pinning it.
//
// Today: --force does not replace the journal, it emits a SECOND history
// alongside the first. The stale events survive, so a stream that was
// badly ordered stays badly ordered at its head, and because --force mints
// fresh event_ids the hub's ON CONFLICT (event_id) dedupe cannot collapse
// them either.
func TestE2E_Backfill_ForceAppendsRatherThanRegenerating(t *testing.T) {
	ctx := context.Background()
	_ = openHub(t) // gate on the DSN and keep hub state isolated

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	a.createNode(t, "PRJX-1", "child")
	a.createNode(t, "PRJX-2", "parent")
	reparentUnder(t, a, "PRJX-1", "PRJX-2", 1)
	a.wipeLocalSyncEvents(t)

	_, err := a.store.Backfill(ctx, false)
	require.NoError(t, err, "first backfill")

	var first int
	require.NoError(t, a.store.WriteDB().QueryRowContext(ctx,
		`SELECT count(*) FROM sync_events`).Scan(&first))
	require.Positive(t, first)

	_, err = a.store.Backfill(ctx, true) // --force
	require.NoError(t, err, "forced backfill")

	var second int
	require.NoError(t, a.store.WriteDB().QueryRowContext(ctx,
		`SELECT count(*) FROM sync_events`).Scan(&second))

	require.Equalf(t, first*2, second,
		"MTIX-89: --force APPENDS a second history (%d -> %d) instead of "+
			"regenerating. If this assertion fails because the count no longer "+
			"doubles, a regenerate path has landed — update this test and the "+
			"operator guidance in cmd/mtix/sync_backfill.go together.", first, second)

	// And the duplicate is a genuinely distinct event, so the hub cannot
	// dedupe it away: the ids differ.
	var distinctIDs int
	require.NoError(t, a.store.WriteDB().QueryRowContext(ctx,
		`SELECT count(DISTINCT event_id) FROM sync_events`).Scan(&distinctIDs))
	require.Equal(t, second, distinctIDs,
		"every appended event carries a fresh event_id, so hub-side "+
			"ON CONFLICT (event_id) dedupe will not collapse the duplicate")
}

// NOTE on re-pushing a regenerated stream (MTIX-89): a third test was
// written here asserting that regenerate + re-push onto a populated hub
// must collide. It was REMOVED because it does not hold. In this fixture
// the push succeeds cleanly and the hub keeps exactly one create per node:
// emitUIDFor carries the node's EXISTING nodes.uid across a re-emit
// (sync_emit.go), so the hub registry recognises the same logical node and
// no-ops rather than demanding a renumber. That is the design working.
//
// A CLI reproduction against a real hub nevertheless failed with
// "resolve renumber ...: settle uid <id>: not found", and the uid in that
// error was a fresh event_id rather than the node's uid — i.e. the create
// self-anchored, which only happens when nodes.uid is empty. Which
// condition produces that (an older store, a specific op ordering, or the
// second project prefix) is not yet pinned down, so no assertion is made
// here. MTIX-89 carries the open question; a test should land with the
// answer, not ahead of it.
