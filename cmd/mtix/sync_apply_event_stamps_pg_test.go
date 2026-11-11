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

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// Real-Postgres proof of replica determinism for stored times (MTIX-112.17.16.13;
// ADR-006 I1): two stores that pull the same hub events with apply clocks years
// apart hold identical created_at and updated_at, and created_at is the origin's.
// Gated on MTIX_PG_TEST_DSN through openCmdHub.

// pullStoreAt builds an empty store whose clock reads at and pulls the whole hub into it.
func pullStoreAt(t *testing.T, pool *transport.Pool, at time.Time) *sqlite.Store {
	t.Helper()
	st, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = st.Close() })
	st.SetClock(func() time.Time { return at })
	var stderr bytes.Buffer
	_, _, err = pullLoop(context.Background(), testIngest(&stderr), pool, st, transport.PullCursor{}, 100)
	require.NoError(t, err, stderr.String())
	return st
}

// nodeStamps returns created_at and updated_at of TEST-1 as the store holds them.
func nodeStamps(t *testing.T, st *sqlite.Store) (created, updated string) {
	t.Helper()
	require.NoError(t, st.QueryRow(context.Background(),
		`SELECT created_at, updated_at FROM nodes WHERE id = 'TEST-1'`).Scan(&created, &updated))
	return created, updated
}

// TestPullLoop_TwoReplicasDifferentClocks_HoldSameCreatedAndUpdatedAt_PG: the
// origin creates a task and edits it; both replicas pull from the real hub and
// agree with each other and with the origin's created_at.
func TestPullLoop_TwoReplicasDifferentClocks_HoldSameCreatedAndUpdatedAt_PG(t *testing.T) {
	pool := openCmdHub(t)
	ctx := context.Background()
	origin, err := sqlite.New(filepath.Join(t.TempDir(), ".mtix"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = origin.Close() })
	editAt := time.Date(2026, 6, 2, 3, 4, 5, 0, time.UTC)
	origin.SetClock(func() time.Time { return editAt })
	require.NoError(t, origin.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "task")))
	desc := "edited"
	require.NoError(t, origin.UpdateNode(ctx, "TEST-1", &store.NodeUpdate{Description: &desc}))
	var stderr bytes.Buffer
	_, err = pushLoop(ctx, &stderr, pool, origin)
	require.NoError(t, err, stderr.String())

	a := pullStoreAt(t, pool, time.Date(2027, 3, 4, 5, 6, 7, 0, time.UTC))
	b := pullStoreAt(t, pool, time.Date(2035, 9, 8, 7, 6, 5, 0, time.UTC))

	createdA, updatedA := nodeStamps(t, a)
	createdB, updatedB := nodeStamps(t, b)
	require.Equal(t, createdA, createdB)
	require.Equal(t, updatedA, updatedB)
	require.Equal(t, "2026-06-01T00:00:00Z", createdA, "created_at is the origin's")
	require.Equal(t, editAt.Format(time.RFC3339), updatedA, "updated_at is the edit event's time")
}
