// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store"
)

// plainStore hides the SQLite store's own purge (PurgeDeletedNode), as a
// store without one, so the scan takes the plain delete.
type plainStore struct{ store.Store }

// TestBackgroundScan_PurgesExpiredNode_WithAndWithoutTheStoresOwnPurge: the
// retention cleanup (FR-3.3a) purges an expired soft-deleted node and keeps
// a live one, through the store's own purge when it has one
// (MTIX-112.17.16.37, lead ruling r2 3) and through the plain delete when
// it has none.
func TestBackgroundScan_PurgesExpiredNode_WithAndWithoutTheStoresOwnPurge(t *testing.T) {
	now := time.Date(2026, 3, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		plain bool
	}{
		{"the store's own purge", false},
		{"a store without its own purge", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			_, s := newTestBackgroundService(t, fixedClock(now))
			var st store.Store = s
			if tt.plain {
				st = plainStore{s}
			}
			bg := service.NewBackgroundService(st, &service.StaticConfig{RetentionDuration: 30 * 24 * time.Hour},
				slog.Default(), fixedClock(now))
			createTestNode(t, s, "PROJ-1", "PROJ", "Expired", now.Add(-40*24*time.Hour))
			createTestNode(t, s, "PROJ-2", "PROJ", "Live", now.Add(-40*24*time.Hour))
			require.NoError(t, s.DeleteNode(ctx, "PROJ-1", false, "admin"))
			_, err := s.WriteDB().ExecContext(ctx, `UPDATE nodes SET deleted_at = ? WHERE id = 'PROJ-1'`,
				now.Add(-31*24*time.Hour).Format(time.RFC3339))
			require.NoError(t, err)

			require.NoError(t, bg.RunScan(ctx))
			var gone, live int
			require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM nodes WHERE id = 'PROJ-1'`).Scan(&gone))
			require.NoError(t, s.QueryRow(ctx, `SELECT COUNT(*) FROM nodes WHERE id = 'PROJ-2'`).Scan(&live))
			require.Zero(t, gone, "the expired node is purged")
			require.Equal(t, 1, live, "the live node is kept")
		})
	}
}
