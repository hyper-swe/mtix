// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"sync"
	"testing"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// --- MTIX-30.4: hub registry + renumber-required push outcome ---
//
// The registry is a DERIVED partial unique index on
// (project_prefix, node_id) WHERE op_type='create_node' over the
// append-only sync_events log (ADR-003 §6). On push, a second
// create_node for an already-registered (project, display_path) yields a
// structured RenumberRequired outcome; the first writer wins and no node
// is ever lost (ADR-003 §6, §9).

// countCreateForNode returns how many create_node rows exist for a node.
func countCreateForNode(t *testing.T, pool *transport.Pool, prefix, nodeID string) int {
	t.Helper()
	var n int
	require.NoError(t, pool.Inner().QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events
		 WHERE project_prefix = $1 AND node_id = $2 AND op_type = 'create_node'`,
		prefix, nodeID).Scan(&n))
	return n
}

// TestRegistry_TwoCreatesSameNumber_OneAcceptedOtherRenumber is the core
// corner case: two distinct create_node events (distinct event_ids)
// claim the SAME (project, display_path). First-writer-wins — one is
// accepted, the other returns a renumber-required outcome — and CRUCIALLY
// the rejected node's event is NOT silently dropped from the caller's
// view: it is surfaced so the claimer can retry the next free number.
func TestRegistry_TwoCreatesSameNumber_OneAcceptedOtherRenumber(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	first := makeEvent("0193fa00-0000-7000-8000-0000000004a1", "MTIX-1.4", "alice", 1)
	_, _, _, err := pool.PushEventsWithRenumbers(context.Background(),
		[]*model.SyncEvent{first})
	require.NoError(t, err)

	// Bob, who had not pulled, mints the same display_path under a
	// distinct create event.
	second := makeEvent("0193fa00-0000-7000-8000-0000000004b2", "MTIX-1.4", "bob", 2)
	accepted, conflicts, renumbers, err := pool.PushEventsWithRenumbers(
		context.Background(), []*model.SyncEvent{second})
	require.NoError(t, err)

	require.Empty(t, accepted, "the colliding create must NOT land")
	require.Empty(t, conflicts, "renumber-required is not a field-level LWW conflict")
	require.Len(t, renumbers, 1, "exactly one renumber-required outcome")

	r := renumbers[0]
	require.Equal(t, second.EventID, r.EventID, "renumber names the rejected event")
	require.Equal(t, "MTIX", r.ProjectPrefix)
	require.Equal(t, "MTIX-1.4", r.DisplayPath, "renumber names the contested path")
	require.Equal(t, first.EventID, r.RegisteredEventID,
		"renumber names the already-registered first-writer that won")

	// First-writer-wins: only the first create survives; no node lost,
	// just one that must renumber.
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-1.4"))
}

// TestRegistry_IdempotentRepush_NoSpuriousRenumber asserts that
// re-pushing the SAME create_node event (same event_id) is a no-op, NOT
// a renumber. Re-push is the common case (retry after a flaky network);
// treating it as a collision would force pointless churn (ADR-003 §6,
// idempotent replay).
func TestRegistry_IdempotentRepush_NoSpuriousRenumber(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ev := makeEvent("0193fa00-0000-7000-8000-0000000004c3", "MTIX-2.1", "alice", 1)

	accepted, _, renumbers, err := pool.PushEventsWithRenumbers(
		context.Background(), []*model.SyncEvent{ev})
	require.NoError(t, err)
	require.Equal(t, []string{ev.EventID}, accepted)
	require.Empty(t, renumbers)

	// Same event again.
	accepted2, _, renumbers2, err := pool.PushEventsWithRenumbers(
		context.Background(), []*model.SyncEvent{ev})
	require.NoError(t, err)
	require.Empty(t, accepted2, "re-push lands nothing new (ON CONFLICT DO NOTHING)")
	require.Empty(t, renumbers2, "re-push of the SAME event must NOT trigger a renumber")

	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-2.1"))
}

// TestRegistry_RejectNeverDeletesAnyNode asserts the ADR-003 §9 liveness
// invariant: a hub reject forces a renumber but can NEVER lose a node.
// A batch that mixes a fresh create, a colliding create, and an unrelated
// create lands BOTH legitimate creates; only the contested one is held
// for renumber. A single collision MUST NOT wedge the rest of the batch
// (ADR-003 §6.1/F-1).
func TestRegistry_RejectNeverDeletesAnyNode(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	// Pre-register MTIX-1.4 from alice.
	_, _, _, err := pool.PushEventsWithRenumbers(context.Background(),
		[]*model.SyncEvent{makeEvent("0193fa00-0000-7000-8000-0000000005a1", "MTIX-1.4", "alice", 1)})
	require.NoError(t, err)

	batch := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-0000000005b1", "MTIX-1.5", "bob", 2),   // fresh, lands
		makeEvent("0193fa00-0000-7000-8000-0000000005b2", "MTIX-1.4", "bob", 3),   // collides, renumber
		makeEvent("0193fa00-0000-7000-8000-0000000005b3", "MTIX-1.6", "bob", 4),   // fresh, lands
	}
	accepted, _, renumbers, err := pool.PushEventsWithRenumbers(context.Background(), batch)
	require.NoError(t, err)

	require.ElementsMatch(t,
		[]string{"0193fa00-0000-7000-8000-0000000005b1", "0193fa00-0000-7000-8000-0000000005b3"},
		accepted, "both non-colliding creates land despite the sibling collision")
	require.Len(t, renumbers, 1)
	require.Equal(t, "0193fa00-0000-7000-8000-0000000005b2", renumbers[0].EventID)

	// No node lost: every distinct node now has its create on the hub.
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-1.4"))
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-1.5"))
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-1.6"))
}

// TestRegistry_TwoCreatesSameNumberInOneBatch covers an intra-batch
// collision: two distinct create events for the same path in a SINGLE
// push. The earlier-positioned create wins; the later is held for
// renumber. (Without the in-tx guard, the partial unique index would
// abort the whole batch.)
func TestRegistry_TwoCreatesSameNumberInOneBatch(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	batch := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-0000000006a1", "MTIX-3.1", "alice", 1),
		makeEvent("0193fa00-0000-7000-8000-0000000006a2", "MTIX-3.1", "bob", 2),
	}
	accepted, _, renumbers, err := pool.PushEventsWithRenumbers(context.Background(), batch)
	require.NoError(t, err)

	require.Equal(t, []string{"0193fa00-0000-7000-8000-0000000006a1"}, accepted,
		"first create in the batch wins")
	require.Len(t, renumbers, 1)
	require.Equal(t, "0193fa00-0000-7000-8000-0000000006a2", renumbers[0].EventID)
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-3.1"))
}

// TestRegistry_DifferentProjectsSameNumberCoexist asserts the registry
// key includes project_prefix: PRJA-1.4 and PRJB-1.4 are distinct nodes
// and both must land (no cross-project renumber).
func TestRegistry_DifferentProjectsSameNumberCoexist(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	a := makeEvent("0193fa00-0000-7000-8000-0000000007a1", "PRJA-1.4", "alice", 1)
	a.ProjectPrefix = "PRJA"
	b := makeEvent("0193fa00-0000-7000-8000-0000000007b1", "PRJB-1.4", "bob", 2)
	b.ProjectPrefix = "PRJB"

	accepted, _, renumbers, err := pool.PushEventsWithRenumbers(
		context.Background(), []*model.SyncEvent{a, b})
	require.NoError(t, err)
	require.Len(t, accepted, 2, "same number under different projects both land")
	require.Empty(t, renumbers)
}

// TestRegistry_NonCreateEventsNotRegistered asserts the index is PARTIAL:
// later non-create events (update_field, transition_status) on an
// already-created node do NOT trip the registry and never produce a
// renumber outcome.
func TestRegistry_NonCreateEventsNotRegistered(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	create := makeEvent("0193fa00-0000-7000-8000-0000000008a1", "MTIX-4.1", "alice", 1)
	upd := makeEvent("0193fa00-0000-7000-8000-0000000008a2", "MTIX-4.1", "alice", 2)
	upd.OpType = model.OpTransitionStatus
	upd.Payload = []byte(`{"from":"todo","to":"in_progress"}`)

	accepted, _, renumbers, err := pool.PushEventsWithRenumbers(
		context.Background(), []*model.SyncEvent{create, upd})
	require.NoError(t, err)
	require.Len(t, accepted, 2, "create + a later non-create on the same node both land")
	require.Empty(t, renumbers, "a non-create event must never trigger a renumber")
}

// TestRegistry_ConcurrentPushesSameNumber stresses the registry under
// concurrent pushers racing the same display_path from distinct events.
// Exactly one create may win; every other pusher must observe a
// renumber-required outcome (never a lost node, never two winners). The
// partial unique index is the durable backstop that makes this hold even
// when the in-tx pre-checks of two transactions both pass before either
// commits.
func TestRegistry_ConcurrentPushesSameNumber(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	const n = 8
	eventIDs := []string{
		"0193fa00-0000-7000-8000-0000000009a1",
		"0193fa00-0000-7000-8000-0000000009a2",
		"0193fa00-0000-7000-8000-0000000009a3",
		"0193fa00-0000-7000-8000-0000000009a4",
		"0193fa00-0000-7000-8000-0000000009a5",
		"0193fa00-0000-7000-8000-0000000009a6",
		"0193fa00-0000-7000-8000-0000000009a7",
		"0193fa00-0000-7000-8000-0000000009a8",
	}

	var (
		mu            sync.Mutex
		totalAccepted int
		totalRenumber int
		wg            sync.WaitGroup
	)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			ev := makeEvent(eventIDs[i], "MTIX-5.1", "author", int64(i+1))
			acc, _, ren, err := pool.PushEventsWithRenumbers(
				context.Background(), []*model.SyncEvent{ev})
			require.NoError(t, err)
			mu.Lock()
			totalAccepted += len(acc)
			totalRenumber += len(ren)
			mu.Unlock()
		}(i)
	}
	wg.Wait()

	// Exactly one create wins on the hub; the registry index guarantees it.
	require.Equal(t, 1, countCreateForNode(t, pool, "MTIX", "MTIX-5.1"),
		"the partial unique index admits exactly one create_node for the number")
	require.Equal(t, 1, totalAccepted, "exactly one pusher's create landed")
	require.Equal(t, n-1, totalRenumber, "every other pusher got renumber-required; no node lost")
}

// TestRegistry_PartialIndexRejectsPreexistingDuplicates documents the
// Phase-1-before-1.5 migration ordering (ADR-003 §7): the partial unique
// index CANNOT be added to a log that already contains duplicate
// (project_prefix, node_id) create events. Projects already bitten by the
// MTIX-28 bug must be swept (Phase 1) before the index is created. We
// assert the failure directly so the ordering constraint is a tested
// invariant, not a comment.
func TestRegistry_PartialIndexRejectsPreexistingDuplicates(t *testing.T) {
	dsn := requireTestDSN(t)
	freshSchema(t, dsn)
	ctx := context.Background()

	pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	// Apply every migration EXCEPT the registry index (009), so we can
	// seed duplicates into a log that the index does not yet forbid.
	files, err := migrations.Files()
	require.NoError(t, err)
	for _, f := range files {
		if f == "009_node_registry_index.sql" {
			continue
		}
		body, rErr := migrations.Read(f)
		require.NoError(t, rErr)
		_, eErr := pool.Inner().Exec(ctx, body)
		require.NoErrorf(t, eErr, "apply %s", f)
	}

	// Seed two create_node rows for the SAME (project, node_id) — the
	// pre-existing-duplicate state of a project bitten by MTIX-28.
	for _, id := range []string{
		"0193fa00-0000-7000-8000-00000000aa01",
		"0193fa00-0000-7000-8000-00000000aa02",
	} {
		_, eErr := pool.Inner().Exec(ctx, `
			INSERT INTO sync_events
			  (event_id, project_prefix, node_id, op_type, payload,
			   wall_clock_ts, lamport_clock, vector_clock,
			   author_id, author_machine_hash)
			VALUES ($1, 'MTIX', 'MTIX-1.4', 'create_node', '{"title":"x"}',
			        1, 1, '{}', 'a', '00')`, id)
		require.NoError(t, eErr)
	}

	// Now adding the registry index MUST fail loudly (unique violation).
	body, err := migrations.Read("009_node_registry_index.sql")
	require.NoError(t, err)
	_, err = pool.Inner().Exec(ctx, body)
	require.Error(t, err,
		"the partial unique index cannot be added over pre-existing duplicates (Phase 1 sweep must run first)")
}
