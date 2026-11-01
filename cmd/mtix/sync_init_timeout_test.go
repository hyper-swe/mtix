// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
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

// Exercise long-budget guidance directly without waiting for a long timer.
func TestSyncInit_MigrationRetryExceedsExpiredBudget(t *testing.T) {
	const maximum = time.Duration(1<<63 - 1)
	for _, budget := range []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, 30 * time.Minute, 2 * time.Hour, maximum - time.Nanosecond} {
		t.Run(budget.String(), func(t *testing.T) {
			err := syncInitMigrationTimeoutError(budget, errors.New("apply schema failed"))
			parts := strings.SplitN(err.Error(), "mtix sync init --migrate-timeout=", 2)
			require.Len(t, parts, 2)
			retry, parseErr := time.ParseDuration(strings.SplitN(parts[1], ",", 2)[0])
			require.NoError(t, parseErr)
			require.Greater(t, retry, budget)
			require.GreaterOrEqual(t, retry, 15*time.Minute)
			if budget >= time.Hour {
				require.LessOrEqual(t, retry-budget, time.Hour, "automatic growth is capped")
			}
		})
	}
}

func TestSyncInit_MigrationRetryAtDurationMaximum(t *testing.T) {
	err := syncInitMigrationTimeoutError(time.Duration(1<<63-1), errors.New("apply schema failed"))
	require.ErrorContains(t, err, "maximum supported duration")
	require.NotContains(t, err.Error(), "retry as the hub owner with mtix sync init")
	require.ErrorContains(t, err, "mtix sync doctor")
}

func TestSyncInit_MigrationTimeoutPreservesPGCause(t *testing.T) {
	server := &pgconn.PgError{Code: "57014", Message: "server cancelled statement"}
	cause := fmt.Errorf("migrate: apply schema: %w", server)
	err := migrateSyncInit(context.Background(), time.Millisecond, func(ctx context.Context) error {
		<-ctx.Done()
		return cause
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var actual *pgconn.PgError
	require.ErrorAs(t, err, &actual)
	require.Same(t, server, actual)
	require.ErrorContains(t, err, "migrate: apply schema")
}
