// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// mkPGNode builds a minimal valid node fixture for seeding a competing store.
func mkPGNode(id, parent string, depth, seq int, title string) *model.Node {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	n := &model.Node{
		ID: id, ParentID: parent, Project: "TEST", Depth: depth, Seq: seq,
		Title: title, NodeType: model.NodeTypeForDepth(depth), Priority: model.PriorityMedium,
		Status: model.StatusOpen, Weight: 1.0, Creator: "other",
		CreatedAt: now, UpdatedAt: now,
	}
	n.ContentHash = n.ComputeHash()
	return n
}

// TestPushLoop_DrainsRenumberRequired is the production-path proof of the
// MTIX-28 fix (ADR-003 §6, MTIX-30.7): when the hub already holds the number
// a local create wants, the REAL pushLoop must re-claim + renumber the node
// locally and re-push it at a distinct number — both nodes preserved, no
// split-brain. (The e2e suite proves the same algorithm via its own helper;
// this exercises the actual cmd/mtix pushLoop + Store.RenumberForHubRejection.)
func TestPushLoop_DrainsRenumberRequired(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t) // app.store == client A, prefix TEST

	ctx := context.Background()

	// Seed the hub so TEST-1.1 is already registered by a DIFFERENT client B
	// (distinct uid). Use a real second store so the seeded create event is
	// genuinely valid; push ONLY B's child create so the parent doesn't also
	// collide (we want to isolate the child-number collision).
	storeB, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = storeB.Close() })
	require.NoError(t, storeB.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "B parent")))
	require.NoError(t, storeB.CreateNode(ctx, mkPGNode("TEST-1.1", "TEST-1", 1, 1, "B child")))

	bEvents, err := readPendingBatch(ctx, storeB, 100)
	require.NoError(t, err)
	var bChild *model.SyncEvent
	for _, e := range bEvents {
		if e.NodeID == "TEST-1.1" && e.OpType == model.OpCreateNode {
			bChild = e
		}
	}
	require.NotNil(t, bChild, "B must have a create_node for TEST-1.1")
	acc, _, _, err := pool.PushEventsWithRenumbers(ctx, []*model.SyncEvent{bChild})
	require.NoError(t, err)
	require.Len(t, acc, 1, "hub registers B's TEST-1.1")

	// Client A independently creates TEST-1 and TEST-1.1 (its own uid).
	require.NoError(t, runCreate("A parent", "", "", 3, "", "", "", "", ""))   // TEST-1
	require.NoError(t, runCreate("A child", "TEST-1", "", 3, "", "", "", "", "")) // TEST-1.1

	// The REAL pushLoop must drain the TEST-1.1 collision: A's parent lands at
	// TEST-1 (free), A's child collides with B's TEST-1.1 -> renumber-required
	// -> re-claimed to TEST-1.2 -> re-pushed.
	var stderr bytes.Buffer
	pushed, _, _, renumbered, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, renumbered, 1, "A's colliding child must be renumbered")
	assert.GreaterOrEqual(t, pushed, 2, "A's parent + renumbered child both land")

	// A's local store: the child moved off TEST-1.1 to TEST-1.2; both A nodes present.
	_, err = app.store.GetNode(ctx, "TEST-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound, "A's child renumbered away from the contested number")
	moved, err := app.store.GetNode(ctx, "TEST-1.2")
	require.NoError(t, err)
	assert.Equal(t, "A child", moved.Title)

	// The hub holds BOTH creates at distinct numbers — no node lost.
	var n11, n12 int
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM sync_events WHERE node_id='TEST-1.1' AND op_type='create_node'`).Scan(&n11))
	require.NoError(t, pool.Inner().QueryRow(ctx,
		`SELECT count(*) FROM sync_events WHERE node_id='TEST-1.2' AND op_type='create_node'`).Scan(&n12))
	assert.Equal(t, 1, n11, "B's original TEST-1.1 preserved")
	assert.Equal(t, 1, n12, "A's renumbered TEST-1.2 landed")
}
