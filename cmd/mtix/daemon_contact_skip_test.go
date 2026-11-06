// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestRunDaemon_ExchangeLockBusy_PreservesLastRealContact(t *testing.T) {
	for _, hook := range []string{"", "1"} {
		t.Run("hook="+hook, func(t *testing.T) {
			src := daemonOutcomeFixture(t)
			t.Setenv("MTIX_SYNC_HOOK", hook)
			dsn, calls := daemonErrorHub(t, "28P01")
			t.Setenv(transport.EnvDSN, dsn)
			prior := oplocal.HubContact{At: src.now(), Route: "poll", OK: true}
			require.NoError(t, src.state.RecordHubContact(src.key(), prior))
			owner, err := exchangeService(app.store).BeginExchangePull(t.Context(), src.mtixDir)
			require.NoError(t, err)
			defer owner.Close()
			runOutcomeDaemonOnce(t)
			require.Zero(t, calls.Load(), "a busy local exchange never dials the hub")
			got, present, err := src.state.ReadHubContact(src.key())
			require.NoError(t, err)
			require.True(t, present)
			require.Equal(t, prior, got, "no hub result may replace the last real outcome")
		})
	}
}

func TestRunDaemon_ShutdownDuringPull_PreservesLastRealContact(t *testing.T) {
	src := daemonOutcomeFixture(t)
	dsn, started := daemonBlackhole(t)
	t.Setenv(transport.EnvDSN, dsn)
	prior := oplocal.HubContact{At: src.now(), Route: "poll", OK: true}
	require.NoError(t, src.state.RecordHubContact(src.key(), prior))
	ctx, cancel := context.WithCancel(t.Context())
	var out, errout bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- runDaemon(ctx, &out, &errout, nil, transport.Options{InsecureTLS: true}, 3600) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(4 * time.Second):
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("real pull did not dial")
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
		done <- nil
	case <-time.After(4 * time.Second):
		t.Fatal("daemon did not stop")
	}
	got, present, err := src.state.ReadHubContact(src.key())
	require.NoError(t, err)
	require.True(t, present)
	require.Equal(t, prior, got, "shutdown is not a hub failure")
}

func daemonBlackhole(t *testing.T) (string, <-chan struct{}) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	started := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case started <- struct{}{}:
			default:
			}
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			_, _ = io.Copy(io.Discard, conn)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { require.NoError(t, ln.Close()); <-done })
	return "postgres://user:skip-secret@" + ln.Addr().String() + "/board?sslmode=disable", started
}

func TestRunOneDaemonPull_CancelDuringRealRequest_NoOutcome(t *testing.T) {
	_ = daemonOutcomeFixture(t)
	dsn, started := daemonBlackhole(t)
	t.Setenv(transport.EnvDSN, dsn)
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan daemonPullOutcome, 1)
	joined := false
	go func() { done <- runOneDaemonPull(ctx, io.Discard, nil, transport.Options{InsecureTLS: true}) }()
	t.Cleanup(func() {
		cancel()
		if !joined {
			select {
			case <-done:
			case <-time.After(4 * time.Second):
				t.Error("pull did not join")
			}
		}
	})
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("real request did not reach the socket")
	}
	cancel()
	select {
	case result := <-done:
		joined = true
		require.Equal(t, daemonPullOutcome{}, result, "shutdown has no completed contact outcome")
	case <-time.After(4 * time.Second):
		t.Fatal("cancelled pull did not return")
	}
}
