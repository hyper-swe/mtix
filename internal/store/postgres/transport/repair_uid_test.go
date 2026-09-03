// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// pgUID reads the stored uid of one create row ("" for NULL).
func pgUID(ctx context.Context, t *testing.T, db *pgxpool.Pool, eventID string) string {
	t.Helper()
	var uid *string
	require.NoError(t, db.QueryRow(ctx,
		`SELECT uid FROM sync_events WHERE event_id = $1`, eventID).Scan(&uid))
	if uid == nil {
		return ""
	}
	return *uid
}

// TestRepairCreateUIDs_StampsOnlyNullAndReportsTheRest is the transport
// contract for MTIX-92: NULL uids get the local uid, populated ones are
// never touched, hub-only nodes are reported, dry-run writes nothing,
// and a second run is a no-op.
func TestRepairCreateUIDs_StampsOnlyNullAndReportsTheRest(t *testing.T) {
	pool := openTestPool(t)
	ctx := context.Background()
	require.NoError(t, pool.Migrate(ctx))

	// Three creates pushed by an old client (no uid), one already carrying
	// a uid, and one under another prefix that must stay untouched.
	old1 := makeEvent("0193fa00-0000-7000-8000-0000000000b1", "MTIX-1", "alice", 1)
	old2 := makeEvent("0193fa00-0000-7000-8000-0000000000b2", "MTIX-2", "alice", 2)
	old3 := makeEvent("0193fa00-0000-7000-8000-0000000000b3", "MTIX-3", "alice", 3)
	stamped := makeEvent("0193fa00-0000-7000-8000-0000000000b4", "MTIX-4", "alice", 4)
	stamped.UID = "uid-for-4"
	other := makeEvent("0193fa00-0000-7000-8000-0000000000b5", "DEMO-1", "alice", 5)
	other.ProjectPrefix = "DEMO"
	ids, _, err := pool.PushEvents(ctx, []*model.SyncEvent{old1, old2, old3, stamped, other})
	require.NoError(t, err)
	require.Len(t, ids, 5)

	local := map[string]string{
		"MTIX-1": "uid-for-1",
		"MTIX-2": "uid-for-2",
		// MTIX-3 is absent locally: hub-only.
		"MTIX-4": "different-uid-for-4", // hub disagrees: mismatch, never overwritten
		"MTIX-9": "uid-for-9",           // local-only, never pushed: not the hub's business
	}

	// Dry run: the plan, and no mutation.
	dry, err := pool.RepairCreateUIDs(ctx, "MTIX", local, true)
	require.NoError(t, err)
	require.True(t, dry.DryRun)
	require.Equal(t, 2, dry.Stamped, "would stamp the two NULL rows with a local uid")
	require.Equal(t, []string{"MTIX-3"}, dry.HubOnly)
	require.Len(t, dry.Mismatched, 1)
	require.Equal(t, "", pgUID(ctx, t, pool.Inner(), old1.EventID), "dry run must write nothing")

	// Apply.
	got, err := pool.RepairCreateUIDs(ctx, "MTIX", local, false)
	require.NoError(t, err)
	require.False(t, got.DryRun)
	require.Equal(t, 2, got.Stamped)
	require.Equal(t, 0, got.AlreadyStamped)
	require.Equal(t, []string{"MTIX-3"}, got.HubOnly, "a hub-only node is reported, not skipped silently")
	require.Equal(t, "MTIX-4", got.Mismatched[0].NodeID)
	require.Equal(t, "uid-for-4", got.Mismatched[0].HubUID)
	require.Equal(t, "different-uid-for-4", got.Mismatched[0].LocalUID)

	require.Equal(t, "uid-for-1", pgUID(ctx, t, pool.Inner(), old1.EventID))
	require.Equal(t, "uid-for-2", pgUID(ctx, t, pool.Inner(), old2.EventID))
	require.Equal(t, "", pgUID(ctx, t, pool.Inner(), old3.EventID), "no local uid, stays NULL")
	require.Equal(t, "uid-for-4", pgUID(ctx, t, pool.Inner(), stamped.EventID), "a populated uid is never overwritten")
	require.Equal(t, "", pgUID(ctx, t, pool.Inner(), other.EventID), "another prefix is out of scope")

	var total int
	require.NoError(t, pool.Inner().QueryRow(ctx, `SELECT count(*) FROM sync_events`).Scan(&total))
	require.Equal(t, 5, total, "nothing is ever deleted")

	// Idempotent: the second run finds the rows stamped and touches nothing.
	again, err := pool.RepairCreateUIDs(ctx, "MTIX", local, false)
	require.NoError(t, err)
	require.Equal(t, 0, again.Stamped)
	require.Equal(t, 2, again.AlreadyStamped)
	require.Equal(t, []string{"MTIX-3"}, again.HubOnly)
}
