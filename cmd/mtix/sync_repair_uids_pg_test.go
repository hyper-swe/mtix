// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// TestRunSyncRepairUIDs_EndToEnd drives the real CLI path for MTIX-92
// against a live hub: push with the fixed client, put the hub into the
// pre-MTIX-91 state (uid NULL on every row), regenerate the local journal,
// and show the re-push FAILS; then dry-run (no change), repair, and show
// the same re-push is a clean no-op. PG-gated; skips without
// MTIX_PG_TEST_DSN.
func TestRunSyncRepairUIDs_EndToEnd(t *testing.T) {
	dsn := requireCmdPG(t)
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	t.Setenv("MTIX_SYNC_DSN", dsn)
	t.Setenv("MTIX_SYNC_HOOK", "")

	// The affected population is the upgrader: a project whose history
	// reached the hub through 'mtix sync backfill'. A backfilled create
	// carries the node's existing uid under a FRESH event id, so once an
	// old client drops the uid in transit the hub knows the node only by
	// an event id the node's uid can never equal. (A node created and
	// pushed by 'mtix create' has uid == create event id, so the hub's
	// NULL fallback happens to resolve it correctly; those need no repair
	// and the repair leaves them AlreadyStamped-equivalent.)
	seedLocal(t, "first", "second") // TEST-1, TEST-2
	regenerateJournal(ctx, t)
	pushLocal(t, dsn)

	// Every release before MTIX-91 pushed uid NULL. Recreate that hub.
	_, err := pool.Inner().Exec(ctx, `UPDATE sync_events SET uid = NULL`)
	require.NoError(t, err)

	// Regenerate the local journal (the MTIX-89 remediation shape) and
	// re-push: the hub sees two unknown nodes claiming taken numbers.
	regenerateJournal(ctx, t)

	var pushErr bytes.Buffer
	_, _, _, _, err = pushLoop(ctx, &pushErr, pool, app.store)
	require.Error(t, err, "RED: a NULL-uid hub must make the re-push collide")
	require.ErrorContains(t, err, "renumber")

	// Dry-run: reports the plan and writes nothing.
	var out, errb bytes.Buffer
	app.jsonOutput = true
	require.NoError(t, runSyncRepairUIDs(ctx, &out, &errb, nil,
		transport.Options{InsecureTLS: true}, "", true), "stderr: %s", errb.String())
	var dry []transport.UIDRepairReport
	require.NoError(t, json.Unmarshal(out.Bytes(), &dry))
	require.Len(t, dry, 1)
	require.True(t, dry[0].DryRun)
	require.Equal(t, 2, dry[0].Stamped)
	require.Equal(t, 2, hubNullUIDCount(ctx, t, pool), "dry-run must not stamp")

	// Apply.
	out.Reset()
	errb.Reset()
	app.jsonOutput = false
	require.NoError(t, runSyncRepairUIDs(ctx, &out, &errb, nil,
		transport.Options{InsecureTLS: true}, "", false), "stderr: %s", errb.String())
	require.Contains(t, out.String(), "TEST: stamped 2 hub create row(s)")
	require.Equal(t, 0, hubNullUIDCount(ctx, t, pool))

	// GREEN: the same re-push is now the same-logical-node no-op.
	pushErr.Reset()
	pushed, _, conflicts, renumbered, err := pushLoop(ctx, &pushErr, pool, app.store)
	require.NoError(t, err, "stderr: %s", pushErr.String())
	require.Equal(t, 2, pushed, "both regenerated creates are accepted as no-ops")
	require.Equal(t, 0, renumbered)
	require.Equal(t, 0, conflicts)

	// Exactly one create per node survives on the hub: nothing duplicated.
	var creates int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM sync_events WHERE op_type = 'create_node'`).Scan(&creates))
	require.Equal(t, 2, creates)
}

func hubNullUIDCount(ctx context.Context, t *testing.T, pool *transport.Pool) int {
	t.Helper()
	var n int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM sync_events WHERE op_type = 'create_node' AND uid IS NULL`).Scan(&n))
	return n
}

// regenerateJournal wipes the local sync journal and backfills it from the
// nodes table: the v0.1.x upgrader's first backfill, and the MTIX-89
// regenerate shape after it.
func regenerateJournal(ctx context.Context, t *testing.T) {
	t.Helper()
	_, err := app.store.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)
	_, err = app.store.Backfill(ctx, false)
	require.NoError(t, err, "backfill")
}
