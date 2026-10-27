// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

func TestWakeBudgetSource_VolatileDatabaseIsRefused(t *testing.T) {
	// Inline isolation avoids testutil's import of this package.
	root := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", root)
	t.Setenv("APPDATA", root)
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	// A volatile connection has no durable location to identify across restores.
	store := &Store{readDB: db}
	source, err := store.WakeBudgetSource(t.Context())
	require.ErrorIs(t, err, model.ErrInvalidInput)
	require.Empty(t, source)
}
