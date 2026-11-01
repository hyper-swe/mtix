// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// A real SELECT-only hub role must not leave a local renumber behind when
// PostgreSQL refuses UPDATE (MTIX-107.46). Uses the isolated role fixture.
func TestCLI_CollisionsResolve_HubRefusesLocalUnchanged(t *testing.T) {
	f := newHardenFixture(t)
	owner := f.ownerRole()
	f.migrateAs(owner) // the migrations must run as a non-superuser owner
	initTestApp(t)
	ctx := context.Background()
	dsn := loginRoleDSN(t, f, owner)
	pool, err := transport.New(ctx, dsn, transport.Options{InsecureTLS: true})
	require.NoError(t, err)
	defer pool.Close()
	seedCrossEpochCollision(t, pool, dsn)
	open, err := pool.ListOpenCollisions(ctx, "TEST")
	require.NoError(t, err)
	require.Len(t, open, 1)
	reader := f.role("reader")
	f.ddl("GRANT SELECT ON ALL TABLES IN SCHEMA public TO %I", reader)
	t.Setenv(transport.EnvDSN, loginRoleDSN(t, f, reader))
	uid := localNodeUID(t, app.store, "TEST-1.1")
	before, err := readPendingBatch(ctx, app.store, 100)
	require.NoError(t, err)
	err = runSyncCollisionsResolve(ctx, &bytes.Buffer{}, &bytes.Buffer{},
		[]string{strconv.FormatInt(open[0].CollisionID, 10)}, transport.Options{InsecureTLS: true}, "incoming")
	require.Error(t, err)
	require.Contains(t, err.Error(), "permission denied")
	var stillAt string
	require.NoError(t, app.store.QueryRow(ctx, `SELECT uid FROM nodes WHERE id = ?`, "TEST-1.1").Scan(&stillAt),
		"the losing task must still be at TEST-1.1: a refused hub update cannot renumber it")
	require.Equal(t, uid, stillAt, "the task at TEST-1.1 is still the losing one")
	after, err := readPendingBatch(ctx, app.store, 100)
	require.NoError(t, err)
	require.Equal(t, before, after, "pending events are unchanged")
	open, err = pool.ListOpenCollisions(ctx, "TEST")
	require.NoError(t, err)
	require.Len(t, open, 1, "hub decision stays open")
}
