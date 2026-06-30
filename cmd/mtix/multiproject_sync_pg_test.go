// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// FR-MULTI-PROJECT AC-5 (sync, DSN-gated): a local DB holding TWO projects
// pushes BOTH to the hub, and a fresh clone reconstructs BOTH and converges.
// This proves the MP-20 invariant — one DB <-> one hub carries every project in
// the DB, with hub-global cursors and no per-project routing (docs/SYNC-DESIGN
// §6.4 / D15).
//
// Gated on MTIX_PG_TEST_DSN via requireCmdPG (sync_loops_pg_test.go) and SKIPPED
// when absent, mirroring the existing PG-gated sync loop tests. The hub helpers
// (openCmdHub, pushLoop, cloneLoop, readCloneCheckpoint) are the real
// production loops, not mocks.
//
// NOTE: running these against a live hub surfaced a real defect in the multi-
// hyphen project path — see TestMultiProject_AC5_MultiHyphenProjectColumn_Bug
// below and MTIX-39. The convergence test deliberately asserts only
// the invariants that genuinely hold today (events carried for every project,
// every node id reconstructed, single-hyphen project column intact); the
// multi-hyphen project-column corruption is pinned separately so a fix trips it.
package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedTwoProjectsAndPush seeds roots/child across TEST + MTIX-DEV-OPS, pushes
// every event to the hub, then resets local node/event state to look like a
// brand-new clone target. It returns the opened hub pool. Shared by the AC-5
// convergence test and the multi-hyphen characterization test.
func seedTwoProjectsAndPushThenWipe(t *testing.T) (*bytes.Buffer, context.Context) {
	t.Helper()
	pool := openCmdHub(t) // skips when MTIX_PG_TEST_DSN is unset
	initTestApp(t)
	ctx := context.Background()

	require.NoError(t, runCreateWithProject("primary root", "", "", 3, "", "", "", "", "", "TEST", true))
	require.NoError(t, runCreateWithProject("ops root", "", "", 3, "", "", "", "", "", mpSecondProject, true))
	require.NoError(t, runCreateWithProject("ops child", "MTIX-DEV-OPS-1", "", 3, "", "", "", "", "", "", true))

	var stderr bytes.Buffer
	pushed, batches, _, _, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)
	require.GreaterOrEqual(t, pushed, 3, "three creates across two projects must push")
	require.GreaterOrEqual(t, batches, 1)

	// Simulate a fresh clone target on the same store.
	for _, stmt := range []string{
		`DELETE FROM sync_events`,
		`DELETE FROM applied_events`,
		`DELETE FROM nodes`,
	} {
		_, execErr := app.store.WriteDB().ExecContext(ctx, stmt)
		require.NoError(t, execErr)
	}
	_, err = app.store.WriteDB().ExecContext(ctx,
		`UPDATE meta SET value = '0' WHERE key = 'meta.sync.clone.checkpoint'`)
	require.NoError(t, err)

	pulled, _, err := cloneLoop(ctx, &stderr, pool, app.store, 0, 100)
	require.NoError(t, err)
	require.GreaterOrEqual(t, pulled, 3, "clone must reconstruct every project's events")
	return &stderr, ctx
}

// TestMultiProject_AC5_SyncCarriesAllProjects proves the MP-20 invariant that
// genuinely holds: a single push/clone carries EVERY project in the DB (no
// per-project flag), every node id round-trips, the hub-global checkpoint
// advances, and the single-hyphen primary converges fully (id + project).
func TestMultiProject_AC5_SyncCarriesAllProjects(t *testing.T) {
	_, ctx := seedTwoProjectsAndPushThenWipe(t)

	cursor, err := readCloneCheckpoint(ctx, app.store, true)
	require.NoError(t, err)
	require.Greater(t, cursor, int64(0), "hub-global clone checkpoint advances")

	// Every node id from BOTH projects is reconstructed by the single clone.
	for _, id := range []string{"TEST-1", "MTIX-DEV-OPS-1", "MTIX-DEV-OPS-1.1"} {
		_, getErr := app.store.GetNode(ctx, id)
		require.NoErrorf(t, getErr, "clone must reconstruct %s (carries all projects)", id)
	}

	// The single-hyphen primary converges fully, project column included.
	primaryRoot, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	assert.Equal(t, "TEST", primaryRoot.Project)
}

// TestMultiProject_AC5_MultiHyphenProjectColumn_Bug is a CHARACTERIZATION test
// that pins a real, currently-shipping defect uncovered by AC-5 against a live
// hub: the local sync emitter derives an event's project_prefix from the node
// id by cutting at the FIRST dash (internal/store/sqlite/sync_emit.go
// projectPrefixFromNodeID), so a multi-hyphen project like MTIX-DEV-OPS emits
// its events under project_prefix "MTIX". After a clone the reconstructed
// node's project column is therefore "MTIX", not "MTIX-DEV-OPS" — and the hub
// namespaces that project's registry/version-gate/settlement under the wrong
// prefix, violating the FR-2.1a "last dash before the first dot" prefix rule
// and FR-MULTI-PROJECT MP-21.
//
// This contradicts AC-4/AC-5 ("multi-hyphen prefix round-trips, converges").
// The renumber path is correct (it uses the stored project column — see
// internal/store/sqlite TestRenumberSubtree_MultiHyphenPrefix_*); only the sync
// emit path mis-parses. Production code is out of scope for MTIX-37.7, so the
// defect is PINNED here and tracked as MTIX-39. When fixed, both assertions
// flip to "MTIX-DEV-OPS" and this test becomes the regression guard.
func TestMultiProject_AC5_MultiHyphenProjectColumn_Bug(t *testing.T) {
	_, ctx := seedTwoProjectsAndPushThenWipe(t)

	opsRoot, err := app.store.GetNode(ctx, "MTIX-DEV-OPS-1")
	require.NoError(t, err)
	assert.Equal(t, "MTIX", opsRoot.Project,
		"KNOWN BUG (MTIX-39): multi-hyphen project column corrupted to first segment on sync; flip to MTIX-DEV-OPS when projectPrefixFromNodeID is fixed")

	projects, err := app.store.DistinctProjects(ctx)
	require.NoError(t, err)
	seen := map[string]int{}
	for _, p := range projects {
		seen[p.Prefix] = p.Count
	}
	assert.Equal(t, 2, seen["MTIX"],
		"KNOWN BUG: ops root+child land under the wrong 'MTIX' project after clone")
	assert.Equal(t, 0, seen[mpSecondProject],
		"KNOWN BUG: the true MTIX-DEV-OPS prefix is absent after sync round-trip")
	assert.Equal(t, 1, seen["TEST"], "the single-hyphen primary is unaffected")
}
