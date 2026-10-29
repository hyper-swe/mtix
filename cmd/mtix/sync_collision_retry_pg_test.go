// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// A local write refusal after acceptance retains the hub choice and can be
// resumed by the same CLI command, including after the local move committed.
func TestCLI_CollisionsResolve_LocalFailureRetry(t *testing.T) {
	pool := openCmdHub(t)
	initTestApp(t)
	ctx := context.Background()
	dsn := requireCmdPG(t)
	seedCrossEpochCollision(t, pool, dsn)
	open, err := pool.ListOpenCollisions(ctx, "TEST")
	require.NoError(t, err)
	require.Len(t, open, 1)
	id := open[0].CollisionID
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `CREATE TRIGGER refuse_collision_move BEFORE UPDATE OF id ON nodes
   BEGIN SELECT RAISE(ABORT, 'local move refused'); END`)
		return err
	}))
	run := func() error {
		return runSyncCollisionsResolve(ctx, &bytes.Buffer{}, &bytes.Buffer{},
			[]string{strconv.FormatInt(id, 10)}, transport.Options{InsecureTLS: true}, "incoming")
	}
	err = run()
	require.Error(t, err)
	require.Contains(t, err.Error(), "hub accepted")
	require.Contains(t, err.Error(), "mtix sync collisions resolve "+strconv.FormatInt(id, 10)+" --winner incoming")
	// The accepted but incomplete collision is discoverable: it left the open
	// list but collisions list shows it as pending, with the resume command.
	pending, err := pool.ListPendingCollisionMoves(ctx, "TEST")
	require.NoError(t, err)
	require.Len(t, pending, 1)
	require.Equal(t, id, pending[0].CollisionID)
	var listed bytes.Buffer
	require.NoError(t, runSyncCollisionsList(ctx, &listed, &bytes.Buffer{}, nil, transport.Options{InsecureTLS: true}, ""))
	require.Contains(t, listed.String(), "waiting for the local move")
	require.Contains(t, listed.String(), "mtix sync collisions resolve "+strconv.FormatInt(id, 10)+" --winner incoming")
	winner, path := hubCollisionDecision(t, dsn, id)
	require.Equal(t, open[0].IncomingEventID, winner)
	require.Empty(t, path)
	_, err = app.store.GetNode(ctx, "TEST-1.1")
	require.NoError(t, err)
	require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `DROP TRIGGER refuse_collision_move`)
		return err
	}))
	require.NoError(t, run())
	require.NoError(t, run())
	pending, err = pool.ListPendingCollisionMoves(ctx, "TEST")
	require.NoError(t, err)
	require.Empty(t, pending, "a completed collision is no longer pending")
	_, path = hubCollisionDecision(t, dsn, id)
	require.NotEqual(t, "TEST-1.1", path)
	_, err = app.store.GetNode(ctx, path)
	require.NoError(t, err)
}

// hubCollisionDecision reads the recorded decision straight from the hub, so
// the test observes behaviour and needs no production accessor.
func hubCollisionDecision(t *testing.T, dsn string, id int64) (winner, path string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err)
	defer func() { require.NoError(t, conn.Close(ctx)) }()
	require.NoError(t, conn.QueryRow(ctx, `SELECT COALESCE(winner_event_id, ''), COALESCE(loser_new_path, '')
 FROM sync_node_collisions WHERE collision_id = $1`, id).Scan(&winner, &path))
	return winner, path
}
