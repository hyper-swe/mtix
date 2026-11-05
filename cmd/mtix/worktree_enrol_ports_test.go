// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func enrolPortsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(worktreeDataDir(root), 0o700))
	return root
}

func TestWorktreeDSN_EnvIgnored_OnlyOwnSecretsFile(t *testing.T) {
	t.Setenv(transport.EnvDSN, "postgres://process-wide.invalid/db")
	root := enrolPortsRoot(t)

	_, err := worktreeDSN(root)
	require.ErrorIs(t, err, os.ErrNotExist, "the process-wide DSN is never a fallback")

	secrets := filepath.Join(root, ".mtix", "secrets")
	require.NoError(t, os.WriteFile(secrets, []byte("postgres://own.invalid/db\n"), 0o600))
	dsn, err := worktreeDSN(root)
	require.NoError(t, err)
	assert.Equal(t, "postgres://own.invalid/db", dsn)

	require.NoError(t, os.Chmod(secrets, 0o644))
	_, err = worktreeDSN(root)
	require.ErrorIs(t, err, transport.ErrSecretsFileMode)

	require.NoError(t, os.WriteFile(secrets, []byte("  \n"), 0o600))
	require.NoError(t, os.Chmod(secrets, 0o600))
	_, err = worktreeDSN(root)
	require.ErrorIs(t, err, transport.ErrDSNNotConfigured)
}

func TestWorktreePorts_SnapshotBackupImport_OnARealStore(t *testing.T) {
	ctx := context.Background()
	root := enrolPortsRoot(t)
	st, err := sqlite.New(worktreeDataDir(root), slog.Default())
	require.NoError(t, err)
	require.NoError(t, st.CreateNode(ctx, mkPGNode("TEST-1", "", 0, 1, "local")))
	require.NoError(t, st.Close())
	ports := newWorktreeEnrolPorts(slog.Default(), io.Discard, transport.Options{}, func(context.Context, service.EnrollmentRequest) error { return nil }, nil)

	snap, err := ports.Snapshot(ctx, worktreeDataDir(root))
	require.NoError(t, err)
	assert.False(t, snap.HubConnected, "a store that never touched a hub is not a hub-board store")
	require.NotEmpty(t, snap.Events)
	assert.Equal(t, "pending", snap.Events[0].Status)
	require.NotNil(t, snap.Carry)

	dest := filepath.Join(t.TempDir(), "bk.db")
	require.NoError(t, ports.Backup(ctx, root, dest))
	assert.FileExists(t, dest)

	require.NoError(t, ports.ImportCarry(ctx, root, &sqlite.InboxCarry{
		Acks: []sqlite.CarriedAck{{AgentID: "a", EventID: "not-here"}},
	}, time.Now()))
	err = ports.ImportCarry(ctx, root, &sqlite.InboxCarry{
		Deliveries: []sqlite.CarriedDelivery{{AgentID: "a", EventID: "not-here", HookName: "h"}},
	}, time.Now())
	require.ErrorIs(t, err, sqlite.ErrCarryEventMissing)
}

func TestWorktreePorts_NoSecrets_HubCallsRefuse(t *testing.T) {
	ctx := context.Background()
	root := enrolPortsRoot(t)
	ports := newWorktreeEnrolPorts(slog.Default(), io.Discard, transport.Options{}, nil, nil)
	_, err := ports.HubMissing(ctx, root, []string{"e"})
	require.ErrorIs(t, err, os.ErrNotExist)
	require.ErrorIs(t, ports.Clone(ctx, root), os.ErrNotExist)
	_, statErr := os.Stat(filepath.Join(worktreeDataDir(root), "mtix.db"))
	assert.ErrorIs(t, statErr, os.ErrNotExist, "a clone that cannot reach its secrets creates no store")
}

func TestWorktreePorts_OpenFailure_Reported(t *testing.T) {
	ports := newWorktreeEnrolPorts(slog.Default(), io.Discard, transport.Options{}, nil, nil)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix"), []byte("a file, not a directory"), 0o600))
	_, err := ports.Snapshot(context.Background(), worktreeDataDir(root))
	require.Error(t, err)
	require.Error(t, ports.Backup(context.Background(), root, filepath.Join(t.TempDir(), "x.db")))
	require.Error(t, ports.ImportCarry(context.Background(), root, nil, time.Now()))
}

// TestWorktreePorts_NeverDiscardLocal: the production port set cannot discard
// local state; the source of the adapter names no discard or reconcile path.
func TestWorktreePorts_NeverDiscardLocal(t *testing.T) {
	src, err := os.ReadFile("worktree_enrol_ports.go")
	require.NoError(t, err)
	lower := strings.ToLower(string(src))
	for _, banned := range []string{"discard", "reconcile", "runsyncclone", "--yes"} {
		assert.NotContains(t, lower, banned)
	}
	_ = model.ErrConflict
}
