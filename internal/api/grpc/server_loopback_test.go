// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package grpc

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestServer_Start_ListensOnLoopbackOnly pins the exposure rule of the gRPC
// server (NFR-5.2, MTIX-220): it listens on 127.0.0.1 and is unreachable on
// any other local address, so a future change cannot widen it silently.
func TestServer_Start_ListensOnLoopbackOnly(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(probe.Addr().String())
	require.NoError(t, err)
	require.NoError(t, probe.Close())

	s := testGRPCServer(t)
	s.config.Port = port
	done := make(chan error, 1)
	go func() { done <- s.Start() }()
	t.Cleanup(func() {
		s.Stop()
		<-done
	})

	require.Eventually(t, func() bool {
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", port), time.Second)
		if dialErr != nil {
			return false
		}
		_ = conn.Close()
		return true
	}, 5*time.Second, 20*time.Millisecond, "the server answers on 127.0.0.1")

	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err)
	checked := 0
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP.IsLoopback() || !ipNet.IP.IsGlobalUnicast() {
			continue
		}
		checked++
		conn, dialErr := net.DialTimeout("tcp", net.JoinHostPort(ipNet.IP.String(), port), time.Second)
		if dialErr == nil {
			_ = conn.Close()
		}
		require.Error(t, dialErr, "the server must not answer on non-loopback address %s", ipNet.IP)
	}
	if checked == 0 {
		t.Log("no non-loopback address on this host: only the loopback answer was verified")
	}
}
