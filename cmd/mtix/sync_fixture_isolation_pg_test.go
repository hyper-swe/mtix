// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// These PG-gated regression tests keep cmd fixtures independent of other
// test packages and processes sharing MTIX_PG_TEST_DSN (MTIX-107.67).
func TestCmdPGFixture_RepeatedLookupKeepsOwnedDatabase(t *testing.T) {
	baseDSN := os.Getenv(envCmdPGTestDSN)
	dsn := requireCmdPG(t)
	base := hubPool(t, baseDSN)
	fixture := hubPool(t, dsn)
	var baseName, fixtureName string
	ctx := context.Background()
	require.NoError(t, base.QueryRow(ctx, `SELECT current_database()`).Scan(&baseName))
	require.NoError(t, fixture.QueryRow(ctx, `SELECT current_database()`).Scan(&fixtureName))
	require.NotEqual(t, baseName, fixtureName, "cmd tests must not reset the shared database")
	require.True(t, strings.HasPrefix(fixtureName, "mtix_cmd_"), "the fixture owns its database")
	require.True(t, dsn == requireCmdPG(t), "repeated lookups must return the same fixture")
	pool := openCmdHub(t)
	var reopenedName string
	require.NoError(t, pool.Inner().QueryRow(ctx, `SELECT current_database()`).Scan(&reopenedName))
	require.Equal(t, fixtureName, reopenedName, "openCmdHub must migrate the fixture already returned")
}

func TestCmdPGFixture_OtherResetDoesNotChangeHubAndCleanupDropsDatabase(t *testing.T) {
	baseDSN := os.Getenv(envCmdPGTestDSN)
	first := openCmdHub(t)
	ctx := context.Background()
	// A marker in the first hub must survive a different fixture's reset.
	_, err := first.Inner().Exec(ctx, `CREATE TABLE fixture_marker (id integer PRIMARY KEY)`)
	require.NoError(t, err)
	_, err = first.Inner().Exec(ctx, `INSERT INTO fixture_marker VALUES (1)`)
	require.NoError(t, err)
	var secondName string
	t.Run("independent fixture", func(t *testing.T) {
		// Subtests inherit process environment; explicitly ask for their own hub.
		t.Setenv("MTIX_CMD_PG_FIXTURE_DSN", "")
		second := openCmdHub(t)
		require.NoError(t, second.Inner().QueryRow(ctx, `SELECT current_database()`).Scan(&secondName))
		var exists bool
		require.NoError(t, second.Inner().QueryRow(ctx,
			`SELECT to_regclass('fixture_marker') IS NOT NULL`).Scan(&exists))
		require.False(t, exists, "a new fixture cannot see another fixture's data")
	})
	var count int
	require.NoError(t, first.Inner().QueryRow(ctx, `SELECT count(*) FROM fixture_marker`).Scan(&count))
	require.Equal(t, 1, count, "resetting another fixture leaves this hub intact")
	base := hubPool(t, baseDSN)
	var exists bool
	// Cleanup has run at the subtest boundary; only its owned database is gone.
	require.NoError(t, base.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, secondName).Scan(&exists))
	require.False(t, exists, "fixture cleanup must drop its owned database")
}
