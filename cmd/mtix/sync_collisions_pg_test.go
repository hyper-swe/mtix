// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"log/slog"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// MTIX-30.8 CLI coverage: `mtix sync mark-restored` and `mtix sync collisions
// list|resolve` against a live hub + a real local store (ADR-003 §6.1, §15).

// localNodeUID reads the durable uid the store assigned a node (ADR-003 §2).
func localNodeUID(t *testing.T, store *sqlite.Store, id string) string {
	t.Helper()
	var uid string
	require.NoError(t, store.QueryRow(context.Background(),
		`SELECT uid FROM nodes WHERE id = ?`, id).Scan(&uid))
	require.NotEmpty(t, uid)
	return uid
}

// TestCLI_MarkRestored_AdvancesEpoch proves the operator command is the only
// epoch mutator: a fresh hub is 0, and mark-restored advances it.
func TestCLI_MarkRestored_AdvancesEpoch(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	dsn := requireCmdPG(t)
	ctx := context.Background()

	epoch, err := pool.CurrentRestoreEpoch(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(0), epoch)

	var out, errBuf bytes.Buffer
	require.NoError(t, runSyncMarkRestored(ctx, &out, &errBuf,
		[]string{dsn}, transport.Options{InsecureTLS: true}))
	assert.Contains(t, out.String(), "restore-epoch is now 1")

	epoch, err = pool.CurrentRestoreEpoch(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(1), epoch, "mark-restored advanced the epoch")
}

// TestCLI_CollisionsList_Empty reports no collisions on a clean hub.
func TestCLI_CollisionsList_Empty(t *testing.T) {
	_ = openCmdHub(t)
	initTestApp(t)
	dsn := requireCmdPG(t)

	var out, errBuf bytes.Buffer
	require.NoError(t, runSyncCollisionsList(context.Background(), &out, &errBuf,
		[]string{dsn}, transport.Options{InsecureTLS: true}, "TEST"))
	assert.Contains(t, out.String(), "no open restore collisions")
}

// seedCrossEpochCollision drives a real cross-epoch collision into the hub:
// client A's TEST-1.1 lands (epoch 0), the operator marks restored (epoch 1),
// then a DISTINCT-uid TEST-1.1 from client B is pushed and BLOCKED — recorded
// as an open collision (held=A epoch 0, incoming=B epoch 1). Returns the pool
// and A's local uid for TEST-1.1 (the node that becomes the loser when the
// admin picks the incoming as winner).
func seedCrossEpochCollision(t *testing.T, pool *transport.Pool, dsn string) {
	t.Helper()
	ctx := context.Background()

	// Client A creates TEST-1 + TEST-1.1 locally and pushes them (epoch 0).
	require.NoError(t, runCreate("A parent", "", "", 3, "", "", "", "", ""))   // TEST-1
	require.NoError(t, runCreate("A child", "TEST-1", "", 3, "", "", "", "", "")) // TEST-1.1
	var stderr bytes.Buffer
	_, _, _, _, err := pushLoop(ctx, &stderr, pool, app.store)
	require.NoError(t, err)

	// Operator advances the epoch (restore-from-backup runbook step).
	_, err = pool.MarkRestored(ctx)
	require.NoError(t, err)

	// Client B independently minted TEST-1.1 (distinct uid). Push only the
	// child create so it cross-epoch-collides with A's held TEST-1.1.
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
	require.NotNil(t, bChild)
	_, _, _, collisions, err := pool.PushEventsWithCollisions(ctx, []*model.SyncEvent{bChild})
	require.NoError(t, err)
	require.Len(t, collisions, 1, "B's cross-epoch TEST-1.1 must be blocked as a restore collision")
}

// TestCLI_CollisionsList_ShowsBothNodes lists the seeded collision with both
// contesting nodes surfaced.
func TestCLI_CollisionsList_ShowsBothNodes(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	dsn := requireCmdPG(t)
	seedCrossEpochCollision(t, pool, dsn)

	var out, errBuf bytes.Buffer
	require.NoError(t, runSyncCollisionsList(context.Background(), &out, &errBuf,
		[]string{dsn}, transport.Options{InsecureTLS: true}, "TEST"))
	s := out.String()
	assert.Contains(t, s, "TEST-1.1", "the contested number is shown")
	assert.Contains(t, s, "held")
	assert.Contains(t, s, "incoming")
	assert.Contains(t, s, "1 open restore collision")
}

// TestCLI_CollisionsResolve_RenumbersLoser_NoNodeLost is the admin-resolve
// happy path (Option B): the admin picks the INCOMING as winner, so the local
// HELD node (A's TEST-1.1) is the loser and renumbers to the next free number.
// Both nodes are preserved; the hub collision is closed.
func TestCLI_CollisionsResolve_RenumbersLoser_NoNodeLost(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	dsn := requireCmdPG(t)
	ctx := context.Background()
	seedCrossEpochCollision(t, pool, dsn)

	open, err := pool.ListOpenCollisions(ctx, "TEST")
	require.NoError(t, err)
	require.Len(t, open, 1)
	collisionID := open[0].CollisionID

	// A's TEST-1.1 is present before resolution.
	_, err = app.store.GetNode(ctx, "TEST-1.1")
	require.NoError(t, err)

	// Admin picks the incoming as winner → the local held node (A's TEST-1.1)
	// loses and must renumber.
	var out, errBuf bytes.Buffer
	require.NoError(t, runSyncCollisionsResolve(ctx, &out, &errBuf,
		[]string{strconv.FormatInt(collisionID, 10), dsn}, transport.Options{InsecureTLS: true}, "incoming"))
	assert.Contains(t, out.String(), "renumbered to TEST-1.2")

	// The loser moved off the contested number; no node lost.
	_, err = app.store.GetNode(ctx, "TEST-1.1")
	assert.ErrorIs(t, err, model.ErrNotFound, "loser renumbered away from the contested number")
	moved, err := app.store.GetNode(ctx, "TEST-1.2")
	require.NoError(t, err)
	assert.Equal(t, "A child", moved.Title, "the loser node is preserved at its new number")

	// Hub collision is closed.
	open, err = pool.ListOpenCollisions(ctx, "TEST")
	require.NoError(t, err)
	assert.Empty(t, open, "the collision is resolved")
}

// TestCLI_CollisionsResolve_RejectsBadWinner validates the --winner flag.
func TestCLI_CollisionsResolve_RejectsBadWinner(t *testing.T) {
	initTestApp(t)
	err := runSyncCollisionsResolve(context.Background(), &bytes.Buffer{}, &bytes.Buffer{},
		[]string{"1", "ignored-dsn"}, transport.Options{}, "bogus")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--winner must be one of")
}
