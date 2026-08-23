// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/stretchr/testify/require"
)

// TestExport_StampsFromTheInjectedClock is the regression for MTIX-70.
//
// Export stamped exported_at with time.Now() directly, bypassing the
// clock the store already carries. Two exports that straddled a second
// boundary therefore produced different bytes and a different checksum
// — which reads as non-determinism, and made
// TestAutoExport_Deterministic_SameContent fail on a loaded CI runner
// while passing on every developer machine.
//
// The determinism claim is not cosmetic: export bytes feed the
// file_hash logging and the replica comparison, so a test asserting it
// has to hold at full strength rather than compare modulo the
// timestamp.
func TestExport_StampsFromTheInjectedClock(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	pinned := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return pinned })

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "n", now)))

	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	require.Equal(t, pinned.Format(time.RFC3339), data.ExportedAt,
		"exported_at must come from the store's clock, not from time.Now()")
}

// TestExport_IsDeterministicAcrossASecondBoundary is the failure the
// incident actually produced, reproduced deliberately: two exports of
// unchanged state, forced into different wall-clock seconds, must be
// byte-identical.
func TestExport_IsDeterministicAcrossASecondBoundary(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	pinned := time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)
	s.SetClock(func() time.Time { return pinned })

	now := time.Now().UTC().Truncate(time.Second)
	require.NoError(t, s.CreateNode(ctx, makeRootNode("PROJ-1", "PROJ", "n", now)))

	first, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	second, err := s.Export(ctx, "", "")
	require.NoError(t, err)

	require.Equal(t, first.ExportedAt, second.ExportedAt)
	require.Equal(t, first.Checksum, second.Checksum)
	require.Equal(t, first.NodeCount, second.NodeCount)
}

// TestExport_DefaultsToWallClock keeps production behavior unchanged:
// exported_at is injectable, NOT constant. The tasks.json re-export diff
// is deliberate and stays.
func TestExport_DefaultsToWallClock(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "wall.db")
	s, err := sqlite.New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })

	ctx := context.Background()
	before := time.Now().UTC().Add(-2 * time.Second)
	data, err := s.Export(ctx, "", "")
	require.NoError(t, err)
	after := time.Now().UTC().Add(2 * time.Second)

	stamped, err := time.Parse(time.RFC3339, data.ExportedAt)
	require.NoError(t, err)
	require.WithinRange(t, stamped, before, after,
		"a store with no clock override still stamps the real wall clock")
}
