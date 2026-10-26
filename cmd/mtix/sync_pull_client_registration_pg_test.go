// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Pull registers the client with the hub (MTIX-95.13, review F-12): a client
// that only pulls was invisible to the version gate and to any stale-client
// report because only push and init wrote sync_project_clients. These tests
// run the real pull against MTIX_PG_TEST_DSN (the harness of
// sync_loops_pg_test.go).

// pullClientVersion is the version the pulling CLI reports in these tests.
const pullClientVersion = "0.6.0"

// useClientVersion makes the CLI report v for the test and restores the build
// version afterwards.
func useClientVersion(t *testing.T, v string) {
	t.Helper()
	old := version
	version = v
	t.Cleanup(func() { version = old })
}

// hubClientVersion reads the cli_version the hub holds for (prefix, this
// machine), or "" when the hub has no such row, over a connection of its own.
func hubClientVersion(t *testing.T, prefix string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv(transport.EnvDSN))
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close(ctx)) }()
	var got string
	err = conn.QueryRow(ctx,
		`SELECT cli_version FROM sync_project_clients WHERE project_prefix = $1 AND machine_hash = $2`,
		prefix, clientMachineHash()).Scan(&got)
	if err == pgx.ErrNoRows {
		return ""
	}
	require.NoError(t, err)
	return got
}

// TestPull_RegistersClientVersion: a pull-only client, one that never pushed
// and never ran init against this hub, is registered by its first pull with
// the board's sync project prefix, its machine hash and the version it runs,
// and the version-gate query then counts it.
func TestPull_RegistersClientVersion(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	useClientVersion(t, pullClientVersion)
	setPeerMeta(t, "meta.sync.project_prefix", "TEST")
	ctx := context.Background()
	pushToHub(t, pool, remoteCreates(t, 5)...)
	require.Empty(t, hubClientVersion(t, "TEST"), "no client row before the pull")
	var stdout, stderr bytes.Buffer

	require.NoError(t, runSyncPull(ctx, &stdout, &stderr, nil, cloudOpts, 100), stderr.String())

	require.Equal(t, pullClientVersion, hubClientVersion(t, "TEST"),
		"the pull recorded the version it runs for this machine")
	counted, err := pool.ProjectAllClientsAtLeast(ctx, "TEST", "0.5.0")
	require.NoError(t, err)
	require.True(t, counted, "the version gate sees the pull-only client and its version")
	stale, err := pool.ProjectAllClientsAtLeast(ctx, "TEST", "0.7.0")
	require.NoError(t, err)
	require.False(t, stale, "a client below the minimum closes the gate")
}

// TestPull_ClientRegistrationFails_WarnsAndPullCompletes: when the
// registration cannot be written (the table is gone from the hub here) the
// pull still applies its events, returns no error and saves its cursor; the
// only trace is one warning that names the grant fix.
func TestPull_ClientRegistrationFails_WarnsAndPullCompletes(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	useClientVersion(t, pullClientVersion)
	setPeerMeta(t, "meta.sync.project_prefix", "TEST")
	ctx := context.Background()
	events := remoteCreates(t, 5)
	pushToHub(t, pool, events...)
	dropHubClientTable(t)
	var stdout, stderr bytes.Buffer

	require.NoError(t, runSyncPull(ctx, &stdout, &stderr, nil, cloudOpts, 100), stderr.String())

	require.Contains(t, stderr.String(), "WARN: pull client registration skipped")
	require.Contains(t, stderr.String(), "INSERT and UPDATE on sync_project_clients")
	require.Contains(t, stdout.String(), "pull complete: 1 events applied")
	requireAppliedOnce(t, events[0])
	requireSavedCursor(t, transport.CursorAt(events[0]))
}

// dropHubClientTable removes sync_project_clients from the hub, so any
// registration write fails while the events and cursor tables are untouched.
func dropHubClientTable(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, os.Getenv(transport.EnvDSN))
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close(ctx)) }()
	_, err = conn.Exec(ctx, `DROP TABLE sync_project_clients`)
	require.NoError(t, err)
}
