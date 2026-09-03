// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// MTIX-92 end to end, against a real Postgres hub.
//
// Every release before the MTIX-91 fix pushed events with uid NULL, and
// the hub registers such a create under its event_id. A fixed client that
// regenerates its journal re-emits each create with the node's real uid,
// which can never equal that event_id, so the hub demands a renumber for a
// number the same node already holds. The repair stamps each hub create
// with its node's uid; afterwards the same re-push is the no-op ADR-003
// §6 intends. The test asserts the failure BEFORE the repair, so the
// repair is proven against the real collision rather than assumed.

func TestE2E_UIDRepair_RegenerateAndRePushIsNoop(t *testing.T) {
	ctx := context.Background()
	pool := openHub(t)

	a := newFakeCLI(t, "A", "aaaaaaaaaaaaaaaa")
	a.createNode(t, "PRJX-1", "child")
	a.createNode(t, "PRJX-2", "parent")
	reparentUnder(t, a, "PRJX-1", "PRJX-2", 1)
	a.registerOnHub(ctx, t, pool)

	// The affected population is the upgrader whose history reached the
	// hub through 'mtix sync backfill': a backfilled create carries the
	// node's existing uid under a FRESH event id. Once an old client drops
	// the uid in transit, the hub knows the node only by that event id,
	// which the node's uid can never equal. (A create pushed straight from
	// creation self-anchors uid == event id, so the NULL fallback happens
	// to resolve it and it never collides.)
	a.wipeLocalSyncEvents(t)
	_, err := a.store.Backfill(ctx, false)
	require.NoError(t, err, "first backfill")

	pushed, conflicts := a.pushAll(ctx, t, pool)
	require.Equal(t, 2, pushed)
	require.Zero(t, conflicts)

	// The hub as every pre-MTIX-91 client left it: uid NULL on every row.
	_, err = pool.Inner().Exec(ctx, `UPDATE sync_events SET uid = NULL`)
	require.NoError(t, err)

	// Regenerate the local journal (MTIX-89's remediation shape).
	a.wipeLocalSyncEvents(t)
	_, err = a.store.Backfill(ctx, false)
	require.NoError(t, err, "regenerate")

	// RED: the hub cannot recognise its own nodes and demands renumbers.
	accepted, _, renumbers, collisions := a.pushOnce(ctx, t, pool)
	require.Empty(t, accepted, "before the repair no regenerated create is accepted")
	require.Len(t, renumbers, 2, "before the repair every regenerated create collides with its own number")
	require.Empty(t, collisions)

	// The repair, exactly as the CLI runs it: local uid map, per prefix.
	uids, err := a.store.NodeUIDsByProject(ctx)
	require.NoError(t, err)
	require.Len(t, uids["PRJX"], 2)

	dry, err := pool.RepairCreateUIDs(ctx, "PRJX", uids["PRJX"], true)
	require.NoError(t, err)
	require.Equal(t, 2, dry.Stamped, "dry-run plans both rows")
	again, _, renumbersStillThere, _ := a.pushOnce(ctx, t, pool)
	require.Empty(t, again)
	require.Len(t, renumbersStillThere, 2, "a dry run must change nothing on the hub")

	rep, err := pool.RepairCreateUIDs(ctx, "PRJX", uids["PRJX"], false)
	require.NoError(t, err)
	require.Equal(t, 2, rep.Stamped)
	require.Empty(t, rep.HubOnly)
	require.Empty(t, rep.Mismatched)

	// GREEN: regenerate + re-push is a clean no-op.
	pushed, conflicts = a.pushAll(ctx, t, pool)
	require.Equal(t, 2, pushed, "the regenerated creates are accepted as same-node no-ops")
	require.Zero(t, conflicts)
	require.Equal(t, 1, hubCreateCount(ctx, t, pool, "PRJX-1"), "exactly one create per node survives")
	require.Equal(t, 1, hubCreateCount(ctx, t, pool, "PRJX-2"))

	// And a fresh clone applies cleanly with the hierarchy intact.
	b := newFakeCLI(t, "B", "bbbbbbbbbbbbbbbb")
	require.Positive(t, b.pullAll(ctx, t, pool))
	require.ElementsMatch(t, []string{"PRJX-1", "PRJX-2"}, b.listNodeIDs(t))
	var parentID string
	require.NoError(t, b.store.WriteDB().QueryRowContext(ctx,
		`SELECT COALESCE(parent_id, '') FROM nodes WHERE id = 'PRJX-1'`).Scan(&parentID))
	require.Equal(t, "PRJX-2", parentID)

	// Idempotent: a second repair stamps nothing.
	rep2, err := pool.RepairCreateUIDs(ctx, "PRJX", uids["PRJX"], false)
	require.NoError(t, err)
	require.Zero(t, rep2.Stamped)
	require.Equal(t, 2, rep2.AlreadyStamped)
}
