// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestRunOneDaemonPull_DirectPreflightError_UsesFailureFallback(t *testing.T) {
	_ = daemonOutcomeFixture(t)
	app.store = nil
	var stderr bytes.Buffer
	result := runOneDaemonPull(t.Context(), &stderr, nil, transport.Options{})
	require.False(t, result.ok, "a direct error bypassing the raw observer must not become success")
	require.Equal(t, "pull_failed", result.class)
	require.Contains(t, stderr.String(), "local store not initialized")
}

func TestSyncPull_HookWarning_DoesNotCompletePull(t *testing.T) {
	_ = daemonOutcomeFixture(t)
	dsn, calls := daemonErrorHub(t, "08006")
	t.Setenv(transport.EnvDSN, dsn)
	t.Setenv("MTIX_SYNC_HOOK", "1")
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, 100))
	require.Positive(t, calls.Load())
	require.Contains(t, stderr.String(), "WARN:")
	require.Empty(t, stdout.String(), "a hook warning must return before completion output")
}

func TestRunOneDaemonPull_QuarantineHookWarning_StillReportsFailure(t *testing.T) {
	initTestApp(t)
	_, _, link := linkDepEvents(t)
	_, err := applyPullBatch(t.Context(), testIngest(nil), app.store, quarantineSourcePull, []*model.SyncEvent{link})
	require.NoError(t, err)
	_, err = app.store.WriteDB().Exec(`CREATE TRIGGER outcome_retry_fault BEFORE UPDATE ON sync_quarantine
 BEGIN SELECT RAISE(ABORT, 'connection reset by peer'); END`)
	require.NoError(t, err)
	dsn, calls := daemonErrorHub(t, "28P01")
	t.Setenv(transport.EnvDSN, dsn)
	t.Setenv("MTIX_SYNC_HOOK", "1")
	var stderr bytes.Buffer
	result := runOneDaemonPull(t.Context(), &stderr, nil, transport.Options{InsecureTLS: true})
	require.Zero(t, calls.Load(), "the quarantine failure happens before hub contact")
	require.Contains(t, stderr.String(), "WARN:")
	require.False(t, result.ok, "nil hook-mode return must not mean successful pull")
	require.Equal(t, "pull_failed", result.class)
}
