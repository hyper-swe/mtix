// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service_test

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutoExport_IdleRepairsHashesWithoutRewritingBoard(t *testing.T) {
	f := newIdleExportFixture(t, slog.LevelDebug)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	first := idleExportBytes(t, path)
	baselinePath := filepath.Join(f.dir, "data", "sync-db.sha256")
	baseline := idleExportBytes(t, baselinePath)
	require.NoError(t, os.WriteFile(baselinePath, []byte("stale baseline"), 0644))
	_, err := f.store.WriteDB().ExecContext(ctx, "DELETE FROM meta WHERE key = 'last_export_hash'")
	require.NoError(t, err)
	f.advance()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	require.Equal(t, string(first), string(idleExportBytes(t, path)))
	require.Equal(t, baseline, idleExportBytes(t, baselinePath))
	assertIdleExportMetadata(t, f, first)
}

func TestAutoExport_IdleMetadataFailureKeepsPendingAndBoard(t *testing.T) {
	f := newIdleExportFixture(t, slog.LevelDebug)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	first := idleExportBytes(t, path)
	baselinePath := filepath.Join(f.dir, "data", "sync-db.sha256")
	require.NoError(t, os.Remove(baselinePath))
	require.NoError(t, os.Mkdir(baselinePath, 0755))
	f.advance()
	require.ErrorContains(t, f.svc.AutoExport(ctx, f.dir), "write db hash")
	require.Equal(t, string(first), string(idleExportBytes(t, path)))
	status, err := f.svc.PendingExports(ctx, f.dir)
	require.NoError(t, err)
	require.Equal(t, 1, status.Count, "failed bookkeeping cannot retire its request")
	require.NoError(t, os.Remove(baselinePath))
	require.NoError(t, f.svc.DrainPendingExport(ctx, f.dir))
	require.Equal(t, string(first), string(idleExportBytes(t, path)))
	assertIdleExportMetadata(t, f, first)
}

func TestAutoExport_MissingPublishedFileIsRecreated(t *testing.T) {
	f := newIdleExportFixture(t, slog.LevelDebug)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	require.NoError(t, os.Remove(path))
	f.advance()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	assertIdleExportMetadata(t, f, idleExportBytes(t, path))
}

func TestForceExport_InvalidPublishedFileIsRepaired(t *testing.T) {
	for _, invalid := range []string{"{broken", `{"version":1,"schema_version":"2.0.0","node_count":0,"checksum":"invalid"}`} {
		t.Run(invalid, func(t *testing.T) {
			f := newIdleExportFixture(t, slog.LevelDebug)
			path := filepath.Join(f.dir, "tasks.json")
			require.NoError(t, os.WriteFile(path, []byte(invalid), 0644))
			require.NoError(t, f.svc.ForceExport(context.Background(), f.dir))
			assertIdleExportMetadata(t, f, idleExportBytes(t, path))
		})
	}
}

func TestForceExport_ExplicitRewriteRetainsItsBehavior(t *testing.T) {
	f := newIdleExportFixture(t, slog.LevelDebug)
	ctx := context.Background()
	require.NoError(t, f.svc.AutoExport(ctx, f.dir))
	path := filepath.Join(f.dir, "tasks.json")
	first := idleExportBytes(t, path)
	f.advance()
	require.NoError(t, f.svc.ForceExport(ctx, f.dir))
	require.NotEqual(t, string(first), string(idleExportBytes(t, path)), "explicit sync --fix retains its requested rewrite")
	assertIdleExportMetadata(t, f, idleExportBytes(t, path))
}
