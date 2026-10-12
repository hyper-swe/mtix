// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestPendingExportDoctor_AgeBoundaryAndErrors(t *testing.T) {
	for _, age := range []time.Duration{time.Hour, time.Hour + time.Nanosecond, -time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			pendingExportDoctorFixture(t)
			ctx := context.Background()
			cutoff, err := app.store.SnapshotExportRequest(ctx)
			require.NoError(t, err)
			require.NoError(t, app.store.RetireExportRequest(ctx, cutoff))
			now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
			require.NoError(t, app.store.MarkExportRequest(ctx, now.Add(-age)))
			check := checkPendingExports(ctx)
			require.True(t, check.Pass)
			require.Equal(t, age > time.Hour, check.Warn)
			require.Equal(t, 1, check.PendingExports.Count)
			if age < 0 {
				require.Zero(t, check.PendingExports.OldestAgeSeconds)
			}
		})
	}
	t.Run("corrupt metadata", func(t *testing.T) {
		pendingExportDoctorFixture(t)
		ctx := context.Background()
		require.NoError(t, app.store.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, "UPDATE meta SET value=? WHERE key=?", "not-json", "meta.export.pending")
			return err
		}))
		check := checkPendingExports(ctx)
		require.False(t, check.Pass, "unreadable intent must not be reported clean")
		require.Nil(t, check.PendingExports)
		require.Contains(t, check.Detail, "read pending exports")
	})
}
