// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"log/slog"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil"
)

func TestWakeBudgetSource_RelativeOpenThenChdir_StaysBoundToDatabase(t *testing.T) {
	testutil.OperatorState(t)
	t.Chdir(t.TempDir())
	store, err := sqlite.New("wake.db", slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	before, err := store.WakeBudgetSource(t.Context())
	require.NoError(t, err)
	t.Chdir(t.TempDir())
	after, err := store.WakeBudgetSource(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after, "the connected database, not the caller's later working directory, determines the accounting source")
}
