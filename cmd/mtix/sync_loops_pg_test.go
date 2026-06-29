// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// These PG-gated tests exercise pushLoop / pullLoop / cloneLoop
// directly. The e2e suite has its own inlined harness that bypasses
// these cobra-package loops, so they never enter the default
// coverage profile. Running the real loops here against a live PG
// closes that gap.

const envCmdPGTestDSN = "MTIX_PG_TEST_DSN"

func requireCmdPG(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv(envCmdPGTestDSN)
	if dsn == "" {
		t.Skipf("set %s to enable PG-gated cmd/mtix loop coverage", envCmdPGTestDSN)
	}
	return dsn
}

func freshCmdHub(t *testing.T, dsn string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cfg, err := pgxpool.ParseConfig(dsn)
	require.NoError(t, err)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	require.NoError(t, err)
	defer pool.Close()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS sync_node_collisions CASCADE`,
		`DROP TABLE IF EXISTS node_renumber_remaps CASCADE`,
		`DROP TABLE IF EXISTS sync_conflicts CASCADE`,
		`DROP TABLE IF EXISTS applied_events CASCADE`,
		`DROP TABLE IF EXISTS sync_events CASCADE`,
		`DROP TABLE IF EXISTS sync_project_clients CASCADE`,
		`DROP TABLE IF EXISTS sync_projects CASCADE`,
		`DROP TABLE IF EXISTS sync_hub_state CASCADE`,
		`DROP TABLE IF EXISTS audit_log CASCADE`,
		`DROP FUNCTION IF EXISTS audit_log_immutable() CASCADE`,
	} {
		_, err := pool.Exec(ctx, stmt)
		require.NoErrorf(t, err, "drop: %s", stmt)
	}
}

func openCmdHub(t *testing.T) *transport.Pool {
	t.Helper()
	dsn := requireCmdPG(t)
	freshCmdHub(t, dsn)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	require.NoError(t, pool.Migrate(ctx))
	return pool
}

// TestPushLoop_EmptyQueueIsNoop confirms the loop returns cleanly when
// there's nothing pending. Covers the first iteration's early exit.
func TestPushLoop_EmptyQueueIsNoop(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)

	var stderr bytes.Buffer
	pushed, batches, conflicts, _, err := pushLoop(context.Background(), &stderr,
		pool, app.store)
	require.NoError(t, err)
	require.Equal(t, 0, pushed)
	require.Equal(t, 0, batches)
	require.Equal(t, 0, conflicts)
}

// TestPushLoop_DrainsPendingEvents seeds local nodes (which emit
// pending events) and confirms pushLoop ships them to the hub.
func TestPushLoop_DrainsPendingEvents(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)

	for i := 0; i < 3; i++ {
		require.NoError(t, runCreate("n"+string(rune('a'+i)), "", "", 3, "", "", "", "", ""))
	}

	var stderr bytes.Buffer
	pushed, batches, _, _, err := pushLoop(context.Background(), &stderr,
		pool, app.store)
	require.NoError(t, err)
	require.GreaterOrEqual(t, pushed, 3,
		"three creates should produce at least 3 pushed events")
	require.GreaterOrEqual(t, batches, 1)
}

// TestPullLoop_EmptyHubReturnsCleanly. Fresh hub → no events to apply.
func TestPullLoop_EmptyHubReturnsCleanly(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)

	var stderr bytes.Buffer
	pulled, batches, err := pullLoop(context.Background(), &stderr,
		pool, app.store, 0, 100)
	require.NoError(t, err)
	require.Equal(t, 0, pulled)
	require.Equal(t, 0, batches)
}

// TestPullLoop_AppliesHubEvents. Push events on one store, then pull
// on a fresh local store via pullLoop.
func TestPullLoop_AppliesHubEvents(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)

	// Seed and push from this CLI.
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(context.Background(), &stderr, pool, app.store)
	require.NoError(t, err)

	// Wipe local applied_events + events so pullLoop has work to do
	// when called with since=0. This simulates a fresh consumer.
	ctx := context.Background()
	_, err = app.store.WriteDB().ExecContext(ctx, `DELETE FROM applied_events`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = '0' WHERE key = 'meta.sync.last_pulled_clock'`)
	require.NoError(t, err)

	pulled, batches, err := pullLoop(ctx, &stderr, pool, app.store, 0, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, pulled, 1)
	require.GreaterOrEqual(t, batches, 1)
}

// TestCloneLoop_EmptyHubReturnsCleanly. Mirror of TestPullLoop_EmptyHubReturnsCleanly
// for the clone path (which uses a separate checkpoint sentinel).
func TestCloneLoop_EmptyHubReturnsCleanly(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)

	var stderr bytes.Buffer
	pulled, batches, err := cloneLoop(context.Background(), &stderr,
		pool, app.store, 0, 100)
	require.NoError(t, err)
	require.Equal(t, 0, pulled)
	require.Equal(t, 0, batches)
}

// TestCloneLoop_AppliesAndCheckpoints. Push from producer, clone on
// fresh consumer, assert events applied AND checkpoint advanced.
func TestCloneLoop_AppliesAndCheckpoints(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)

	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(context.Background(), &stderr, pool, app.store)
	require.NoError(t, err)

	// Wipe local state to simulate a fresh clone target.
	ctx := context.Background()
	_, err = app.store.WriteDB().ExecContext(ctx, `DELETE FROM applied_events`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx, `DELETE FROM nodes`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = '0' WHERE key = 'meta.sync.clone.checkpoint'`)
	require.NoError(t, err)

	pulled, _, err := cloneLoop(ctx, &stderr, pool, app.store, 0, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, pulled, 1)

	cursor, err := readCloneCheckpoint(ctx, app.store, true)
	require.NoError(t, err)
	require.Greater(t, cursor, int64(0),
		"clone checkpoint must advance past initial 0 after pull")
}

// --- runSyncPush / runSyncPull / runSyncClone cobra RunE happy paths ---
//
// The error paths (no project, no store) are tested without PG; the
// happy paths need PG and exercise the bulk of the function body
// (resolve DSN → acquire pushlock → open transport.Pool → run loop).

func TestRunSyncPush_HappyPath(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t) // drop + migrate; we re-open below via the DSN.
	initTestApp(t)

	// Seed 2 pending events.
	require.NoError(t, runCreate("p1", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("p2", "", "", 3, "", "", "", "", ""))

	var stdout, stderr bytes.Buffer
	err := runSyncPush(context.Background(), &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true}, false /*force*/)
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "push complete",
		"stdout must report push completion")
}

func TestRunSyncPull_HappyPath(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)

	// Seed + push some events so pull has work to do.
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncPush(context.Background(), &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true}, false))

	// Reset local applied state so pull has events to apply.
	ctx := context.Background()
	_, err := app.store.WriteDB().ExecContext(ctx, `DELETE FROM applied_events`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = '0' WHERE key = 'meta.sync.last_pulled_clock'`)
	require.NoError(t, err)

	stdout.Reset()
	stderr.Reset()
	err = runSyncPull(ctx, &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true}, 100)
	require.NoError(t, err)
	require.Contains(t, stdout.String(), "pull complete")
}

func TestRunSyncClone_HappyPath(t *testing.T) {
	dsn := requireCmdPG(t)
	_ = openCmdHub(t)
	initTestApp(t)

	// Seed + push.
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncPush(context.Background(), &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true}, false))

	// Reset local state so clone has work to do. Clone refuses if
	// sync_events is non-empty (the fresh-clone invariant); wipe it
	// + applied_events + nodes to look like a brand-new CLI.
	ctx := context.Background()
	for _, stmt := range []string{
		`DELETE FROM sync_events`,
		`DELETE FROM applied_events`,
		`DELETE FROM nodes`,
	} {
		_, execErr := app.store.WriteDB().ExecContext(ctx, stmt)
		require.NoError(t, execErr)
	}
	_, ckptErr := app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = '0' WHERE key = 'meta.sync.clone.checkpoint'`)
	require.NoError(t, ckptErr)

	stdout.Reset()
	stderr.Reset()
	require.NoError(t, runSyncClone(ctx, &stdout, &stderr,
		[]string{dsn}, transport.Options{InsecureTLS: true}, false /*resume*/, 100))
	require.Contains(t, stdout.String(), "clone complete")
}
