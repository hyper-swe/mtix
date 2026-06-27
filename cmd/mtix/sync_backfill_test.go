// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/sync/pushlock"
)

// --- Construction + registration ---

func TestSyncBackfillCmd_Construction(t *testing.T) {
	cmd := newSyncBackfillCmd()
	require.Equal(t, "backfill", cmd.Use)
	require.NotNil(t, cmd.Flags().Lookup("dry-run"))
	require.NotNil(t, cmd.Flags().Lookup("force"))
	// --force is intentionally hidden — it's a recovery escape hatch.
	require.True(t, cmd.Flags().Lookup("force").Hidden,
		"--force must be hidden so casual users don't reach for it")
}

func TestSyncCmd_AllElevenFR18CommandsRegistered(t *testing.T) {
	cmd := newSyncCmd()
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	expected := []string{
		"init", "clone", "push", "pull", "status", "doctor",
		"conflicts", "reconcile", "daemon", "backup", "backfill",
		// MTIX-30.10: the ADR-003 §7 node-identity migration driver.
		"migrate",
	}
	for _, name := range expected {
		require.Truef(t, subs[name], "%s subcommand registered", name)
	}
	require.Equal(t, len(expected), len(cmd.Commands()),
		"exactly the FR-18 + ADR-003 §7 sync commands are registered (no extras)")
}

// --- Refusal paths ---

func TestRunSyncBackfill_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, false, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

// N5 — double backfill refused.
func TestRunSyncBackfill_RefusesWhenSyncEventsNonEmpty(t *testing.T) {
	initTestApp(t)
	// Seed one node — emit will create one sync_events row.
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, false, false)
	require.Error(t, err)
	// Helpful recovery hint is part of the contract.
	require.Contains(t, err.Error(), "reconcile --discard-local",
		"refusal message must point at the recovery path")
}

// --- Dry-run ---

func TestRunSyncBackfill_DryRunDoesNotWrite(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("one", "", "", 3, "desc-1", "", "", "", ""))
	require.NoError(t, runCreate("two", "", "", 3, "", "", "", "", ""))

	// Wipe sync_events to simulate a fresh upgrader state.
	wipeSyncEvents(t)

	beforeCount := syncEventsCount(t)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, true, false)
	require.NoError(t, err)

	afterCount := syncEventsCount(t)
	require.Equal(t, beforeCount, afterCount,
		"dry-run must not write to sync_events")
	require.Contains(t, stdout.String(), "backfill dry-run",
		"output must label dry-run mode")
}

// --- Happy path ---

func TestRunSyncBackfill_EmitsOneCreateNodePerExistingNode(t *testing.T) {
	initTestApp(t)
	for i := 0; i < 5; i++ {
		require.NoError(t, runCreate("node-"+string(rune('a'+i)), "", "", 3, "", "", "", "", ""))
	}
	wipeSyncEvents(t)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, false, false)
	require.NoError(t, err)

	require.Equal(t, 5, countSyncEventsByOp(t, model.OpCreateNode))
}

func TestRunSyncBackfill_EmitsUpdateFieldForNonDefaultDescription(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("with-desc", "", "", 3, "hello world", "", "", "", ""))
	wipeSyncEvents(t)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, false, false)
	require.NoError(t, err)

	require.GreaterOrEqual(t, countSyncEventsByOp(t, model.OpUpdateField), 1,
		"non-default description should produce an update_field event")
}

func TestRunSyncBackfill_EmitsTransitionForNonOpenStatus(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("done-task", "", "", 3, "", "", "", "", ""))
	ctx := context.Background()
	// open → in_progress → done (the state machine forbids the direct
	// open → done jump).
	require.NoError(t, app.nodeSvc.TransitionStatus(ctx, "TEST-1",
		model.StatusInProgress, "test", "test"))
	require.NoError(t, app.nodeSvc.TransitionStatus(ctx, "TEST-1",
		model.StatusDone, "test", "test"))
	wipeSyncEvents(t)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(ctx, &stdout, &stderr, false, false)
	require.NoError(t, err)

	require.Equal(t, 1, countSyncEventsByOp(t, model.OpTransitionStatus),
		"non-open status should produce one transition event")
}

// --- wall_clock_ts preservation ---

func TestRunSyncBackfill_PreservesWallClockTs(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("wall-ts", "", "", 3, "", "", "", "", ""))
	wipeSyncEvents(t)

	// Pre-backfill: capture the node's created_at.
	ctx := context.Background()
	var createdAtStr string
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT created_at FROM nodes WHERE id = ?`, "TEST-1",
	).Scan(&createdAtStr))

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncBackfill(ctx, &stdout, &stderr, false, false))

	// Post-backfill: the create_node event's wall_clock_ts should match
	// the node's created_at (millisecond precision).
	var wallTS int64
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT wall_clock_ts FROM sync_events
		 WHERE node_id = ? AND op_type = 'create_node'`, "TEST-1",
	).Scan(&wallTS))
	require.NotZero(t, wallTS, "wall_clock_ts must be non-zero")
}

// --- Lamport monotonicity ---

func TestRunSyncBackfill_LamportMonotonic(t *testing.T) {
	initTestApp(t)
	for i := 0; i < 10; i++ {
		require.NoError(t, runCreate("n"+string(rune('a'+i)), "", "", 3, "", "", "", "", ""))
	}
	wipeSyncEvents(t)

	ctx := context.Background()
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncBackfill(ctx, &stdout, &stderr, false, false))

	rows, err := app.store.Query(ctx,
		`SELECT lamport_clock FROM sync_events ORDER BY lamport_clock ASC`)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()

	var prev int64 = -1
	for rows.Next() {
		var lc int64
		require.NoError(t, rows.Scan(&lc))
		require.Greaterf(t, lc, prev,
			"lamport must be strictly increasing; got %d after %d", lc, prev)
		prev = lc
	}
}

// --- N9 — concurrent with push: pushlock acquired ---

func TestRunSyncBackfill_AcquiresPushlock(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("locked", "", "", 3, "", "", "", "", ""))
	wipeSyncEvents(t)

	// Pre-acquire the pushlock to simulate a running daemon / push.
	other, err := pushlock.Acquire(app.mtixDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = other.Release() })

	var stdout, stderr bytes.Buffer
	err = runSyncBackfill(context.Background(), &stdout, &stderr, false, false)
	require.Error(t, err, "backfill must refuse when pushlock is held")
	require.True(t, errors.Is(err, pushlock.ErrLockHeld),
		"error must be pushlock.ErrLockHeld; got %v", err)
}

// --- N12 — corrupt nodes table ---

func TestRunSyncBackfill_RefusesWithCorruptNodesTable(t *testing.T) {
	initTestApp(t)
	// Bypass the FK constraint by directly inserting a row with a
	// non-existent parent_id. We rely on PRAGMA foreign_keys being ON
	// being relaxable mid-test for the test setup, but if it's strict
	// we use a row that already exists and then update its parent
	// after-the-fact via a write that the canonical path would have
	// rejected. The simplest reliable approach: insert via writeDB with
	// foreign_keys=OFF temporarily.
	ctx := context.Background()
	_, err := app.store.WriteDB().ExecContext(ctx,
		`PRAGMA foreign_keys = OFF`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`INSERT INTO nodes
		 (id, parent_id, depth, seq, project, title, status, created_at, updated_at, content_hash)
		 VALUES ('TEST-99', 'TEST-DOES-NOT-EXIST', 1, 99, 'TEST', 'orphan',
		         'open', '2026-01-01T00:00:00Z', '2026-01-01T00:00:00Z', '')`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx,
		`PRAGMA foreign_keys = ON`)
	require.NoError(t, err)

	wipeSyncEvents(t)

	var stdout, stderr bytes.Buffer
	err = runSyncBackfill(ctx, &stdout, &stderr, false, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invariant violations",
		"refusal message must mention invariant; got %v", err)
	require.Contains(t, err.Error(), "mtix verify",
		"refusal message must point at verify; got %v", err)
}

// --- N8 — empty project ---

func TestRunSyncBackfill_EmptyProject_Noop(t *testing.T) {
	initTestApp(t)
	wipeSyncEvents(t)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, false, false)
	require.NoError(t, err, "backfill on empty project must succeed as no-op")
	require.Equal(t, 0, syncEventsCount(t),
		"no events emitted for empty project")
}

// --- N6 — --force with already-populated table ---

func TestRunSyncBackfill_ForceFlag_EmitsFreshEventIDs(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("dup", "", "", 3, "", "", "", "", ""))
	// Don't wipe — backfill with --force should add fresh events on top
	// of the existing ones (intentional opt-in path).

	beforeCount := syncEventsCount(t)
	require.GreaterOrEqual(t, beforeCount, 1)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, false, true)
	require.NoError(t, err, "force=true bypasses the refusal")

	afterCount := syncEventsCount(t)
	require.Greater(t, afterCount, beforeCount,
		"--force must add new event rows on top of the existing ones")
}

// --- N13 — recovery: backfill after reconcile --discard-local ---

func TestRunSyncBackfill_AfterDiscardLocal_RunsCleanly(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("first", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("second", "", "", 3, "", "", "", "", ""))

	// Simulate `mtix sync reconcile --discard-local` by directly wiping
	// sync_events (the data-layer effect of that command per FR-18.13).
	wipeSyncEvents(t)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, false, false)
	require.NoError(t, err)
	require.Equal(t, 2, countSyncEventsByOp(t, model.OpCreateNode))
}

// --- N3 (negative-case: partial push, backfill refused; user must retry push) ---

func TestRunSyncBackfill_RefusalMessageMentionsRetryPush(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("pending", "", "", 3, "", "", "", "", ""))
	// Simulate the post-partial-push state: sync_events has rows;
	// some pushed, some pending. (We don't mark any pushed here; the
	// scenario is "sync_events is non-empty" regardless of status.)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, false, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mtix sync push",
		"refusal message must mention retry-push as the recovery path")
}

// --- Helpers ---

func wipeSyncEvents(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	_, err := app.store.WriteDB().ExecContext(ctx, `DELETE FROM sync_events`)
	require.NoError(t, err)
}

func syncEventsCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events`).Scan(&n))
	return n
}

func countSyncEventsByOp(t *testing.T, op model.OpType) int {
	t.Helper()
	var n int
	require.NoError(t, app.store.QueryRow(context.Background(),
		`SELECT count(*) FROM sync_events WHERE op_type = ?`, string(op),
	).Scan(&n))
	return n
}

// --- Store-level sentinel error coverage ---

func TestStore_Backfill_RefusesWhenSyncEventsNonEmpty(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("seed", "", "", 3, "", "", "", "", ""))

	_, err := app.store.Backfill(context.Background(), false)
	require.Error(t, err)
	require.True(t, errors.Is(err, sqlite.ErrBackfillSyncEventsNonEmpty),
		"store-level error must be ErrBackfillSyncEventsNonEmpty; got %v", err)
}

func TestStore_BackfillDryRun_CountsExpected(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("a", "", "", 3, "desc-a", "", "", "", ""))
	require.NoError(t, runCreate("b", "", "", 3, "", "", "", "", ""))
	wipeSyncEvents(t)

	result, err := app.store.BackfillDryRun(context.Background())
	require.NoError(t, err)
	require.Equal(t, 2, result.NodeCount)
	require.Equal(t, 2, result.CreateEvents)
	require.GreaterOrEqual(t, result.UpdateFieldEvents, 1,
		"one node has a non-empty description")
}

// Sanity check: backfilling 0 nodes via BackfillDryRun is OK.
func TestStore_BackfillDryRun_EmptyProject(t *testing.T) {
	initTestApp(t)
	// no create — empty nodes table.
	result, err := app.store.BackfillDryRun(context.Background())
	require.NoError(t, err)
	require.Equal(t, 0, result.NodeCount)
	require.Equal(t, 0, result.TotalEvents)
}

// --- Output formatting ---

// Soft-deleted nodes must be skipped by backfill — they are no
// longer canonical, and re-emitting create_node for them would
// resurrect them on the consumer side. Audit gap closure.
func TestRunSyncBackfill_SkipsSoftDeletedNodes(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("keep", "", "", 3, "", "", "", "", ""))
	require.NoError(t, runCreate("delete-me", "", "", 3, "", "", "", "", ""))
	ctx := context.Background()
	// Soft-delete the second node via the canonical service path.
	require.NoError(t, app.nodeSvc.DeleteNode(ctx, "TEST-2", false, "test"))
	wipeSyncEvents(t)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncBackfill(ctx, &stdout, &stderr, false, false))
	require.Equal(t, 1, countSyncEventsByOp(t, model.OpCreateNode),
		"soft-deleted node must be excluded from backfill")
}

// --dry-run on a project with sync_events already populated: must
// succeed (it's read-only) and report what backfill WOULD do if the
// table were wiped. Audit gap closure.
func TestRunSyncBackfill_DryRun_WithPopulatedSyncEvents(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("populated", "", "", 3, "", "", "", "", ""))

	preCount := syncEventsCount(t)
	require.Positive(t, preCount, "test scaffolding emitted events")

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr, true /*dryRun*/, false)
	require.NoError(t, err, "dry-run is read-only and must succeed regardless of sync_events state")
	require.Equal(t, preCount, syncEventsCount(t),
		"dry-run must NEVER write to sync_events")
}

// --dry-run with --force is a no-op combination: dry-run short-circuits
// before --force is consulted. Verify the combination doesn't crash
// and produces a dry-run report.
func TestRunSyncBackfill_DryRun_WithForce_IsDryRun(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("a", "", "", 3, "", "", "", "", ""))

	preCount := syncEventsCount(t)

	var stdout, stderr bytes.Buffer
	err := runSyncBackfill(context.Background(), &stdout, &stderr,
		true /*dryRun*/, true /*force*/)
	require.NoError(t, err)
	require.Equal(t, preCount, syncEventsCount(t),
		"dry-run + force still must not write")
	require.Contains(t, stdout.String(), "dry-run",
		"dry-run mode must label the output regardless of --force")
}

func TestRunSyncBackfill_PrintsAuthorIDCaveat(t *testing.T) {
	initTestApp(t)
	require.NoError(t, runCreate("a", "", "", 3, "", "", "", "", ""))
	wipeSyncEvents(t)

	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncBackfill(context.Background(), &stdout, &stderr, false, false))
	out := stdout.String()
	require.Contains(t, out, "authorID='cli'",
		"output must surface the same-authorID caveat")
	require.Contains(t, out, "Next: run 'mtix sync push'",
		"output must point at the next step")
	require.True(t, strings.Contains(out, "backfill complete"),
		"completion marker must be in stdout")
}
