// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestRunOneDaemonPull_RealTransportEdges_ReportSpecificClass(t *testing.T) {
	for _, tc := range []struct{ mode, class string }{{"eof", "unreachable"}, {"startup-eof", "unreachable"}, {"tls-refused", "tls"}, {"tls-alert", "tls"}} {
		t.Run(tc.mode, func(t *testing.T) {
			_ = daemonOutcomeFixture(t)
			dsn, calls := outcomeWireBreak(t, tc.mode)
			t.Setenv(transport.EnvDSN, dsn)
			result := runOneDaemonPull(t.Context(), io.Discard, nil, transport.Options{InsecureTLS: true})
			require.Positive(t, calls.Load())
			require.True(t, result.attempted)
			require.False(t, result.ok)
			require.Equal(t, tc.class, result.class)
		})
	}
}

func TestRunOneDaemonPull_RealInvocation_HasSixtySecondCeiling(t *testing.T) {
	_ = daemonOutcomeFixture(t)
	dsn, calls := daemonErrorHub(t, "28P01")
	t.Setenv(transport.EnvDSN, dsn)
	observed := false
	began := time.Now()
	result := runOneDaemonPull(context.Background(), io.Discard, nil, transport.Options{InsecureTLS: true}, daemonPullControl{
		onConnect: func(ctx context.Context) {
			observed = true
			deadline, ok := ctx.Deadline()
			require.True(t, ok)
			require.WithinDuration(t, began.Add(60*time.Second), deadline, time.Second)
		},
	})
	require.True(t, observed)
	require.Positive(t, calls.Load())
	require.False(t, result.ok)
}

func outcomeWireBreak(t *testing.T, mode string) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	calls := new(atomic.Int32)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			calls.Add(1)
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			breakOutcomeWire(conn, mode)
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { require.NoError(t, ln.Close()); <-done })
	sslmode := "disable"
	if mode == "tls-refused" || mode == "tls-alert" {
		sslmode = "require"
	}
	return "postgres://user:edge-secret@" + ln.Addr().String() + "/board?sslmode=" + sslmode, calls
}

func breakOutcomeWire(conn net.Conn, mode string) {
	if mode == "eof" {
		return
	}
	var header [4]byte
	if _, err := io.ReadFull(conn, header[:]); err != nil {
		return
	}
	size := binary.BigEndian.Uint32(header[:])
	if size < 4 || size > 65536 {
		return
	}
	_, _ = io.CopyN(io.Discard, conn, int64(size-4))
	switch mode {
	case "tls-refused":
		_, _ = conn.Write([]byte{'N'})
	case "tls-alert":
		_, _ = conn.Write([]byte{'S'})
		peer := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12})
		_ = peer.Handshake()
	}
}

// Publication must finish before another real pull can take exchange ownership (MTIX-112.20.7).
func TestRunOneDaemonPull_OutcomePublishedUnderExchangeOwnership(t *testing.T) {
	_ = daemonOutcomeFixture(t)
	dsn, _ := daemonErrorHub(t, "28P01")
	t.Setenv(transport.EnvDSN, dsn)
	called := false
	result := runOneDaemonPull(t.Context(), io.Discard, nil, transport.Options{InsecureTLS: true}, daemonPullControl{
		onOutcome: func(outcome daemonPullOutcome) {
			called = true
			require.True(t, outcome.attempted)
			owner, err := exchangeService(app.store).BeginExchangePull(t.Context(), app.mtixDir)
			if owner != nil {
				owner.Close()
			}
			require.ErrorIs(t, err, service.ErrExchangeLockBusy)
		},
	})
	require.True(t, called)
	require.True(t, result.reported)
}
