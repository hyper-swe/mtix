// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSyncInit_MigrationTimeoutFlag(t *testing.T) {
	cmd := newSyncInitCmd()
	flag := cmd.Flags().Lookup("migrate-timeout")
	require.NotNil(t, flag)
	require.Equal(t, "5m0s", flag.DefValue)
	require.NoError(t, cmd.Flags().Set("migrate-timeout", "2m"))
}

func TestSyncInit_InvalidMigrationTimeout(t *testing.T) {
	for _, value := range []string{"0s", "-1s"} {
		t.Run(value, func(t *testing.T) {
			cmd := newSyncInitCmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--migrate-timeout", value})
			err := cmd.ExecuteContext(context.Background())
			require.ErrorContains(t, err, "--migrate-timeout must be positive")
		})
	}
}

func TestSyncInit_MigrateUsesConfiguredBudget(t *testing.T) {
	err := migrateSyncInit(context.Background(), 100*time.Millisecond, func(ctx context.Context) error {
		require.NoError(t, ctx.Err())
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.Greater(t, time.Until(deadline), 50*time.Millisecond)
		return nil
	})
	require.NoError(t, err)
}

func TestSyncInit_MigrateDeadlineNamesRecovery(t *testing.T) {
	err := migrateSyncInit(context.Background(), time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "--migrate-timeout=1ms")
	require.ErrorContains(t, err, "mtix sync init --migrate-timeout=15m")
	require.ErrorContains(t, err, "then run mtix sync doctor")
}

func TestSyncInit_MigratePreservesParentDeadline(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := migrateSyncInit(ctx, time.Second, func(ctx context.Context) error { return ctx.Err() })
	require.ErrorIs(t, err, context.Canceled)
	parent, stop := context.WithDeadline(context.Background(), time.Time{})
	defer stop()
	err = migrateSyncInit(parent, time.Second, func(ctx context.Context) error { return ctx.Err() })
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotContains(t, err.Error(), "--migrate-timeout", "caller deadline is distinct from the migration flag")
}

func TestSyncInit_MigratePreservesFailure(t *testing.T) {
	failure := errors.New("migration rejected")
	err := migrateSyncInit(context.Background(), time.Second, func(context.Context) error { return failure })
	require.ErrorIs(t, err, failure)
	require.NotContains(t, err.Error(), "--migrate-timeout")
}
