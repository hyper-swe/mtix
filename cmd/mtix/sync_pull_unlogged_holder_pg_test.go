// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// Two-clone proof of MTIX-112.17.16.39 against a real Postgres hub (gated on
// the cmd PG DSN). Clone B creates TEST-1 and pushes it; the peer (app.store)
// holds a different TEST-1, with a child and a dependency, written with no
// create in its log; the real `mtix sync pull` on the peer must keep both.

// TestRunSyncPull_HubCreateOnNumberOfUnloggedLocalTask_KeepsBothTasks: the
// pull renumbers the peer's task, subtree and references to TEST-3 (its uid
// kept), applies B's TEST-1, and loses or quarantines nothing.
func TestRunSyncPull_HubCreateOnNumberOfUnloggedLocalTask_KeepsBothTasks(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "hub task from B")))
	f.pushB(t)

	require.NoError(t, app.store.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "local task")))
	require.NoError(t, app.store.CreateNode(ctx, mkPGNode("TEST-1.1", "TEST-1", 1, 1, "local child")))
	require.NoError(t, app.store.CreateNode(ctx, mkPGNode("TEST-2", "", 0, 2, "local sibling")))
	require.NoError(t, app.store.AddDependency(ctx, &model.Dependency{
		FromID: "TEST-2", ToID: "TEST-1.1", DepType: model.DepTypeBlocks}))
	_, err := app.store.WriteDB().ExecContext(ctx, `DELETE FROM sync_events WHERE op_type = 'create_node'`)
	require.NoError(t, err)
	// A change of the local task queued before the pull: it names the task by
	// uid, so it follows the task to its new number.
	edited := "edited on the peer"
	require.NoError(t, app.store.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Description: &edited}))
	uid := func(id string) string {
		n, getErr := app.store.GetNode(ctx, id)
		require.NoError(t, getErr)
		return n.UID
	}
	localUID, childUID := uid("TEST-1"), uid("TEST-1.1")

	stdout, errOut := f.pullPeerStreams(t, 100)
	require.Contains(t, stdout, "renumbered: TEST-1 -> TEST-3 local task", "the pull prints the move of the task")
	require.Contains(t, stdout, "renumbered: TEST-1.1 -> TEST-3.1 local child", "and of its child")

	byUID := func(u string) string {
		var id string
		require.NoError(t, app.store.QueryRow(ctx, `SELECT id FROM nodes WHERE uid = ?`, u).Scan(&id))
		return id
	}
	require.Equal(t, "TEST-3", byUID(localUID), "the local task moved to the next free number: %s", errOut)
	require.Equal(t, "TEST-3.1", byUID(childUID))
	hub, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "hub task from B", hub.Title, "the hub's task takes the number")
	var deps, held int
	require.NoError(t, app.store.QueryRow(ctx,
		`SELECT COUNT(*) FROM dependencies WHERE from_id = 'TEST-2' AND to_id = 'TEST-3.1'`).Scan(&deps))
	require.Equal(t, 1, deps)
	require.NoError(t, app.store.QueryRow(ctx, `SELECT COUNT(*) FROM sync_quarantine`).Scan(&held))
	require.Zero(t, held, "nothing is quarantined")

	// The peer then pushes, and B pulls: the re-addressed change must not
	// land on B's TEST-1 (the hub's task), whatever the number was before.
	hubBefore, err := f.b.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	f.pushPeer(t)
	var bErr bytes.Buffer
	_, _, err = pullLoop(ctx, testIngest(&bErr), f.pool, f.b, transport.PullCursor{}, 100)
	require.NoError(t, err, "B pull: %s", bErr.String())
	hubAfter, err := f.b.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, hubBefore.Title, hubAfter.Title, "the hub's task keeps its title")
	require.Equal(t, hubBefore.Description, hubAfter.Description, "the peer's change is not applied to the hub's task")
	require.NotEqual(t, edited, hubAfter.Description)
}

// TestRunSyncPull_HubCreateOnNumberOfUnloggedHolderWithPushedChild_HoldsTheHubCreate:
// the holder has no create in its log, but its child's creation was pushed, so
// the holder cannot move. The hub's create is held in the quarantine with a
// reason that names the number and the local holder, and the pull says so.
func TestRunSyncPull_HubCreateOnNumberOfUnloggedHolderWithPushedChild_HoldsTheHubCreate(t *testing.T) {
	f := newSweepFixture(t)
	ctx := context.Background()
	require.NoError(t, f.b.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "hub task from B")))
	f.pushB(t)

	require.NoError(t, app.store.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "local task")))
	require.NoError(t, app.store.CreateNode(ctx, mkPGNode("TEST-1.1", "TEST-1", 1, 1, "local child")))
	_, err := app.store.WriteDB().ExecContext(ctx, `DELETE FROM sync_events WHERE op_type = 'create_node' AND node_id = 'TEST-1'`)
	require.NoError(t, err)
	_, err = app.store.WriteDB().ExecContext(ctx, `UPDATE sync_events SET sync_status = 'pushed' WHERE op_type = 'create_node' AND node_id = 'TEST-1.1'`)
	require.NoError(t, err)
	holder, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)

	stdout, errOut := f.pullPeerStreams(t, 100)

	require.Contains(t, stdout, "quarantine: 1 pulled events held", "the pull says the hub create is held: %s", errOut)
	var reason string
	require.NoError(t, app.store.QueryRow(ctx, `SELECT reason FROM sync_quarantine`).Scan(&reason))
	require.Contains(t, reason, "TEST-1 is held by local task "+holder.UID)
	kept, err := app.store.GetNode(ctx, "TEST-1")
	require.NoError(t, err)
	require.Equal(t, "local task", kept.Title, "the holder keeps its number")
	require.NotContains(t, stdout, "renumbered:")
}
