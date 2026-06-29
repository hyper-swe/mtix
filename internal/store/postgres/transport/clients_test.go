// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// seedClient upserts a client row with an explicit last_seen_at so the
// active-window edge cases (clock skew, stale re-appearance) can be
// reproduced deterministically.
func seedClient(t *testing.T, pool *transport.Pool, prefix, machine, version string, seen time.Time) {
	t.Helper()
	_, err := pool.Inner().Exec(context.Background(), `
		INSERT INTO sync_project_clients (project_prefix, machine_hash, cli_version, last_seen_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (project_prefix, machine_hash)
		DO UPDATE SET cli_version = EXCLUDED.cli_version, last_seen_at = EXCLUDED.last_seen_at`,
		prefix, machine, version, seen)
	require.NoError(t, err)
}

// EDGE: no clients registered yet → the gate is CLOSED (we cannot prove
// every CLI is compatible when we know of none).
func TestProjectAllClientsAtLeast_NoClients(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.False(t, ok, "no clients yet must keep the gate CLOSED")
}

// CORNER: mixed-version project → gate CLOSED (one stale client below min).
func TestProjectAllClientsAtLeast_MixedVersion_Closed(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	now := time.Now().UTC()
	seedClient(t, pool, "MTIX", "machineA", "0.2.0", now)
	seedClient(t, pool, "MTIX", "machineB", "0.1.9", now) // below min

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.False(t, ok, "one below-min client must close the gate")
}

// CORNER: all clients upgraded → gate OPEN.
func TestProjectAllClientsAtLeast_AllUpgraded_Open(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	now := time.Now().UTC()
	seedClient(t, pool, "MTIX", "machineA", "0.2.0", now)
	seedClient(t, pool, "MTIX", "machineB", "0.3.1", now)
	seedClient(t, pool, "MTIX", "machineC", "0.10.0", now) // double-digit minor

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.True(t, ok, "every client at/above min must open the gate")
}

// CORNER: a stale old-version client re-appears → gate RE-CLOSES. After
// everyone upgraded (gate open), an old client pushes again (its row is
// upserted back to an old version, fresh last_seen) and the gate closes.
func TestProjectAllClientsAtLeast_StaleClientReappears_ReCloses(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	now := time.Now().UTC()
	seedClient(t, pool, "MTIX", "machineA", "0.2.0", now)
	seedClient(t, pool, "MTIX", "machineB", "0.2.0", now)
	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.True(t, ok, "gate open after both upgraded")

	// machineB downgrades / an old build re-appears with a fresh last_seen.
	seedClient(t, pool, "MTIX", "machineB", "0.1.5", now)
	ok, err = pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.False(t, ok, "a re-appearing old client must re-close the gate")
}

// EDGE: a stale client whose last_seen is OUTSIDE the active window is
// ignored, so it does not hold the gate closed forever. With only that
// stale client below min and a fresh client at min, the gate is OPEN.
func TestProjectAllClientsAtLeast_StaleOutsideWindowIgnored(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	now := time.Now().UTC()
	seedClient(t, pool, "MTIX", "machineA", "0.2.0", now)
	// machineB last seen long ago and below min — outside the active window.
	seedClient(t, pool, "MTIX", "machineB", "0.1.0",
		now.Add(-2*transport.ClientActiveWindow))

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.True(t, ok, "a client outside the active window must not hold the gate closed")
}

// EDGE: clock skew — a client with a future last_seen is still counted
// (it is trivially inside the active window). Below-min + future skew
// keeps the gate closed.
func TestProjectAllClientsAtLeast_FutureSkewCounted(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	now := time.Now().UTC()
	seedClient(t, pool, "MTIX", "machineA", "0.2.0", now)
	seedClient(t, pool, "MTIX", "machineB", "0.1.0", now.Add(1*time.Hour)) // future skew, below min

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.False(t, ok, "a future-skewed below-min client must still close the gate")
}

// EDGE: the gate is scoped per project — a below-min client on another
// project must not affect this project's gate.
func TestProjectAllClientsAtLeast_ScopedPerProject(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	now := time.Now().UTC()
	seedClient(t, pool, "MTIX", "machineA", "0.2.0", now)
	seedClient(t, pool, "OTHER", "machineZ", "0.0.1", now) // below min, different project

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.True(t, ok, "another project's stale client must not affect this gate")
}

// A malformed cli_version stored on the hub must close the gate (fail
// safe) rather than error out the whole migration check.
func TestProjectAllClientsAtLeast_UnparseableVersionClosesGate(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	now := time.Now().UTC()
	seedClient(t, pool, "MTIX", "machineA", "0.2.0", now)
	seedClient(t, pool, "MTIX", "machineB", "garbage", now)

	ok, err := pool.ProjectAllClientsAtLeast(context.Background(), "MTIX", "0.2.0")
	require.NoError(t, err)
	require.False(t, ok, "an unparseable stored version must fail safe (gate closed)")
}

// UpsertProjectClient inserts then updates the same (project, machine)
// row in place, advancing cli_version and last_seen_at.
func TestUpsertProjectClient_InsertThenUpdate(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ctx := context.Background()
	require.NoError(t, pool.UpsertProjectClient(ctx, "MTIX", "machineA", "0.1.0"))

	var version string
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT cli_version FROM sync_project_clients WHERE project_prefix=$1 AND machine_hash=$2`,
		"MTIX", "machineA").Scan(&version))
	require.Equal(t, "0.1.0", version)

	require.NoError(t, pool.UpsertProjectClient(ctx, "MTIX", "machineA", "0.2.0"))

	var n int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM sync_project_clients WHERE project_prefix=$1 AND machine_hash=$2`,
		"MTIX", "machineA").Scan(&n))
	require.Equal(t, 1, n, "upsert must update in place, not insert a duplicate")

	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT cli_version FROM sync_project_clients WHERE project_prefix=$1 AND machine_hash=$2`,
		"MTIX", "machineA").Scan(&version))
	require.Equal(t, "0.2.0", version, "upsert must advance the version")
}

// EDGE: empty required args are rejected before any SQL runs.
func TestUpsertProjectClient_EmptyArgsRejected(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ctx := context.Background()
	require.Error(t, pool.UpsertProjectClient(ctx, "", "m", "0.2.0"))
	require.Error(t, pool.UpsertProjectClient(ctx, "MTIX", "", "0.2.0"))
	require.Error(t, pool.UpsertProjectClient(ctx, "MTIX", "m", ""))

	var n int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM sync_project_clients`).Scan(&n))
	require.Equal(t, 0, n, "rejected upserts must not write rows")
}

// A SQL-level failure (here: the table is gone) surfaces as a wrapped,
// DSN-redacted error rather than a panic — exercises the exec error path.
func TestUpsertProjectClient_SQLErrorWrapped(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ctx := context.Background()
	_, err := pool.Inner().Exec(ctx, `DROP TABLE sync_project_clients`)
	require.NoError(t, err)

	err = pool.UpsertProjectClient(ctx, "MTIX", "m", "0.2.0")
	require.Error(t, err)
	require.Contains(t, err.Error(), "upsert project client")
}

// A query-level failure (table dropped) surfaces as a wrapped error.
func TestProjectAllClientsAtLeast_QueryErrorWrapped(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ctx := context.Background()
	_, err := pool.Inner().Exec(ctx, `DROP TABLE sync_project_clients`)
	require.NoError(t, err)

	ok, err := pool.ProjectAllClientsAtLeast(ctx, "MTIX", "0.2.0")
	require.Error(t, err)
	require.False(t, ok)
}

// Pushing events records the calling client's version inside the push tx.
func TestPushEvents_RecordsClientVersion(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ctx := context.Background()
	pool.SetClientIdentity("machineXYZ", "0.2.5")

	events := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-0000000000a1", "MTIX-1", "alice", 1),
	}
	_, _, err := pool.PushEvents(ctx, events)
	require.NoError(t, err)

	var version, machine string
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT cli_version, machine_hash FROM sync_project_clients WHERE project_prefix=$1`,
		"MTIX").Scan(&version, &machine))
	require.Equal(t, "0.2.5", version)
	require.Equal(t, "machineXYZ", machine)
}

// When no client identity is set (e.g. a tool that never called
// SetClientIdentity), push must NOT write a client row and MUST NOT
// error — the gate simply stays closed for that project.
func TestPushEvents_NoIdentity_NoClientRow(t *testing.T) {
	pool := openTestPool(t)
	require.NoError(t, pool.Migrate(context.Background()))

	ctx := context.Background()
	events := []*model.SyncEvent{
		makeEvent("0193fa00-0000-7000-8000-0000000000b1", "MTIX-1", "alice", 1),
	}
	_, _, err := pool.PushEvents(ctx, events)
	require.NoError(t, err)

	var n int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM sync_project_clients`).Scan(&n))
	require.Equal(t, 0, n, "no identity set ⇒ no client row written")
}
