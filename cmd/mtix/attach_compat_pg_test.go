// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// The cmd PostgreSQL matrix also exercises the real released-reader store fixture.
func TestAttachSchema_PostgresPull_PreservesLocalState(t *testing.T) {
	dsn := requireCmdPG(t)
	dir, err := os.Getwd()
	require.NoError(t, err)
	cmd := exec.CommandContext(t.Context(), "go", "test", "./internal/store/sqlite", "-run", "^TestAttachSchema_054BinaryOpensMigratedStore$", "-count=1", "-v")
	cmd.Dir = filepath.Clean(filepath.Join(dir, "..", ".."))
	cmd.Env = append(hostguard.Environ(), "MTIX_ATTACH_TEST_DSN="+dsn)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "released-reader fixture: %s", output)
	t.Logf("released-reader child pid=%d output:\n%s", cmd.Process.Pid, output)
	require.Contains(t, string(output), "ATTACH_COMPAT_REMOTE_APPLIED", "fixture must prove a real applied remote event")
	t.Log("released-reader remote apply and local-state preservation verified")
}

func TestStoreInstanceID_SyncClone_PreservesNewTargetIdentity(t *testing.T) {
	pool := openCmdHub(t)
	producer, err := sqlite.New(filepath.Join(t.TempDir(), "producer.db"), slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, producer.Close()) })
	nodeID := seedAttachExport(t, producer)
	events, err := producer.ReadPendingEvents(t.Context(), 100)
	require.NoError(t, err)
	accepted, conflicts, err := pool.PushEvents(t.Context(), events)
	require.NoError(t, err)
	require.Empty(t, conflicts)
	require.Len(t, accepted, len(events))
	initTestApp(t)
	target := app.store
	before, err := target.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NotEmpty(t, before)
	var stderr bytes.Buffer
	pulled, _, err := cloneLoop(t.Context(), &stderr, pool, app.store, transport.PullCursor{}, 100)
	require.NoError(t, err)
	require.Positive(t, pulled)
	got, err := target.GetNode(t.Context(), nodeID)
	require.NoError(t, err)
	require.Equal(t, "imported task", got.Title)
	after, err := target.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.Equal(t, before, after)
	other, err := producer.StoreInstanceID(t.Context())
	require.NoError(t, err)
	require.NotEqual(t, other, after)
}
