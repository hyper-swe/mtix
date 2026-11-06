// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"io"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestPullContact_DelayedDaemonFailure_DoesNotHideNewerManualSuccess(t *testing.T) {
	_ = openCmdHub(t)
	goodDSN := os.Getenv(transport.EnvDSN)
	src := daemonOutcomeFixture(t)
	useClientVersion(t, "0.6.0")
	setPeerMeta(t, "meta.sync.project_prefix", "TEST")
	badDSN, _ := daemonErrorHub(t, "28P01")
	t.Setenv(transport.EnvDSN, badDSN)
	pulled, resume, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(resume) }) }
	ctx, cancel := context.WithCancel(t.Context())
	go func() {
		defer close(done)
		_ = runDaemon(ctx, io.Discard, io.Discard, nil, transport.Options{InsecureTLS: true}, 5, daemonPassControl{
			afterPull: func() { close(pulled); <-resume }, observed: func(bool) { cancel() },
		})
	}()
	t.Cleanup(func() {
		release()
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("daemon callback did not join")
		}
	})
	select {
	case <-pulled:
	case <-time.After(8 * time.Second):
		t.Fatal("daemon did not finish the real failed pull")
	}
	t.Setenv(transport.EnvDSN, goodDSN)
	require.NoError(t, runSyncPull(ctx, io.Discard, io.Discard, nil, transport.Options{InsecureTLS: true}, 100))
	newer, present, err := src.state.ReadHubContact(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.True(t, newer.OK)
	release()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("daemon callback did not finish")
	}
	final, present, err := src.state.ReadHubContact(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, newer, final, "an older daemon failure must not replace the successful recovery")
}

func TestPullContact_OldFutureTimestamp_CannotPinFailedRecovery(t *testing.T) {
	_ = openCmdHub(t)
	src := daemonOutcomeFixture(t)
	useClientVersion(t, "0.6.0")
	setPeerMeta(t, "meta.sync.project_prefix", "TEST")
	require.NoError(t, src.state.RecordHubContact(src.key(), oplocal.HubContact{At: time.Now().Add(24 * time.Hour), Route: "poll", ErrorClass: "auth"}))
	require.NoError(t, runSyncPull(t.Context(), io.Discard, io.Discard, nil, transport.Options{InsecureTLS: true}, 100))
	got, present, err := src.state.ReadHubContact(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.True(t, got.OK, "a clock rollback must not prevent a real successful recovery")
}
