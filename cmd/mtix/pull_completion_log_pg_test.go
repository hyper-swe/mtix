// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestRunOneDaemonPull_DirectCompletionError_LogScrubsKnownPassword(t *testing.T) {
	_ = openCmdHub(t)
	_ = daemonOutcomeFixture(t)
	useClientVersion(t, "0.6.0")
	setPeerMeta(t, "meta.sync.project_prefix", "TEST")
	app.mtixDir = filepath.Join(app.mtixDir, "direct-log-secret")
	require.NoError(t, os.MkdirAll(filepath.Join(app.mtixDir, "data", "exchange-record.json"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(app.mtixDir, "secrets"), []byte("postgres://user:direct-log-secret@private.invalid/db"), 0o600))
	var stderr bytes.Buffer
	result := runOneDaemonPull(t.Context(), &stderr, nil, transport.Options{InsecureTLS: true})
	require.False(t, result.ok)
	require.Equal(t, "pull_failed", result.class, stderr.String())
	require.Contains(t, stderr.String(), "pull error (will retry)")
	require.NotContains(t, stderr.String(), "direct-log-secret")
}

func TestSyncPull_OutcomeWriteRefused_WarnsWithoutChangingPullResult(t *testing.T) {
	_ = openCmdHub(t)
	src := daemonOutcomeFixture(t)
	useClientVersion(t, "0.6.0")
	setPeerMeta(t, "meta.sync.project_prefix", "TEST")
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(app.mtixDir, "operator-state"))
	var stdout, stderr bytes.Buffer
	require.NoError(t, runSyncPull(t.Context(), &stdout, &stderr, nil, transport.Options{InsecureTLS: true}, 100))
	require.Contains(t, stdout.String(), "pull complete")
	require.Contains(t, stderr.String(), "contact outcome")
	_, present, err := src.state.ReadHubContact(src.key())
	require.NoError(t, err)
	require.False(t, present, "a refused operator write must not fabricate a record")
}
