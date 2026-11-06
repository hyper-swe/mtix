// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func laneRow(t *testing.T, agent, harness, lane string) sqlite.AttachPending {
	t.Helper()
	raw, err := json.Marshal(map[string]string{"agent_id": agent, "harness": harness, "lane_path": lane})
	require.NoError(t, err)
	return sqlite.AttachPending{Kind: "lane", Payload: string(raw)}
}

// The printed command is built from the queued registration, so running it
// keeps the harness (a bare register would record it as unknown).
func TestStrandedRegisterFix_BuiltFromQueuedRows_KeepsAgentHarnessAndLane(t *testing.T) {
	rows := []sqlite.AttachPending{
		laneRow(t, "sb", "pi", "/work/lane one"),
		{Kind: "list", Payload: laneRow(t, "sb", "pi", "/work/lane one").Payload}, // the list row repeats the registration
		laneRow(t, "other", "unknown", "/work/two"),
	}
	assert.Equal(t,
		"cd '/work/lane one' && mtix agent register 'sb' --harness 'pi' ; cd '/work/two' && mtix agent register 'other'"+
			"   # run from a normal shell outside any sandbox, so the host lists the board; add --session <handle> if the session was registered with one",
		strandedRegisterFix(rows))
}

func TestStrandedRegisterFix_HostilePayloads_AreSkippedNeverPrinted(t *testing.T) {
	rows := []sqlite.AttachPending{
		laneRow(t, "x; rm -f /", "pi", "/work"),
		laneRow(t, "ok", "pi", "relative/path"),
		laneRow(t, "ok", "pi", "/work/../etc"),
		{Kind: "lane", Payload: "not json"},
	}
	got := strandedRegisterFix(rows)
	assert.Equal(t, "mtix agent register <agent-id> --harness <harness>   # run it for the agent that registered, from a normal shell in this checkout (outside any sandbox), so the host lists the board", got)
	assert.NotContains(t, got, "rm -f")

	hostileHarness := strandedRegisterFix([]sqlite.AttachPending{laneRow(t, "ok", "pi; id", "/work")})
	assert.Equal(t, "cd '/work' && mtix agent register 'ok'   # run from a normal shell outside any sandbox, so the host lists the board; add --session <handle> if the session was registered with one", hostileHarness)
}

// An unlisted linked worktree that holds its own store and queue is a board of
// its own: it is declined as unlisted, so doctor shows the stranded-queue row
// with the exact command, and the glue never reads or drains that queue.
func TestDoctorGlue_ProductionDeps_UnlistedWorktreeWithOwnQueue_ShowsStrandedRow(t *testing.T) {
	saveAndResetApp(t)
	st := testutil.OperatorState(t)
	mainRoot := listedBoardCheckout(t, st)
	wt := linkedWorktreeOf(t, mainRoot)
	initStoreAt(t, wt)
	enqueueRow(t, wt, ownedLaneRow(t, wt, "wt-agent", "pi", wt))
	t.Chdir(wt)
	store, err := sqlite.New(wt+"/.mtix/data", slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	app.store, app.mtixDir = store, wt+"/.mtix"

	deps, err := productionAttachDoctorDeps(t.Context())
	require.NoError(t, err)
	row, ok := attachDoctorRows(t, deps, false)["glue.stranded-queue"]
	require.True(t, ok, "the worktree's own queue is stranded until it is listed")
	assert.Contains(t, row.Fix, "cd '"+wt+"' && mtix agent register 'wt-agent' --harness 'pi'")
	assert.Equal(t, attach.ReasonUnlisted, deps.glue(t.Context()).Reason)
}

// Doctor reads the queue of the store the predicate resolved and only when it
// is the board doctor itself opened; it never opens a second store to look.
func TestProductionPendingRows_OnlyForTheBoardDoctorOpened(t *testing.T) {
	saveAndResetApp(t)
	opened, resolved := t.TempDir(), t.TempDir()
	initStoreAt(t, opened)
	initStoreAt(t, resolved)
	enqueueRow(t, opened, ownedLaneRow(t, opened, "a", "pi", opened))
	enqueueRow(t, resolved, ownedLaneRow(t, resolved, "b", "pi", resolved))
	store, err := sqlite.New(opened+"/.mtix/data", slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	app.store, app.mtixDir = store, opened+"/.mtix"

	rows, err := productionPendingRows(t.Context(), attach.GlueDecision{StoreRoot: opened})
	require.NoError(t, err)
	assert.Len(t, rows.Valid, 1, "the opened board's own queue")
	rows, err = productionPendingRows(t.Context(), attach.GlueDecision{StoreRoot: resolved})
	require.NoError(t, err)
	assert.Empty(t, rows.Valid, "another board's queue is not read through the opened store")
	rows, err = productionPendingRows(t.Context(), attach.GlueDecision{})
	require.NoError(t, err)
	assert.Empty(t, rows.Valid)
}
