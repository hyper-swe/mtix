// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"crypto/tls"
	"io"
	"net"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// startUntrustedTLSServer listens on loopback, answers each connection's
// SSLRequest with 'S' and then offers a self-signed certificate that no
// test CA signed, so certificate verification always fails. It returns
// the port.
func startUntrustedTLSServer(t *testing.T) string {
	t.Helper()
	certPEM, keyPEM := selfSignedPEM(t, "untrusted test server")
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ln.Close() })
	serverTLS := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(10 * time.Second))
				sslRequest := make([]byte, 8)
				if _, readErr := io.ReadFull(c, sslRequest); readErr != nil {
					return
				}
				if _, writeErr := c.Write([]byte{'S'}); writeErr != nil {
					return
				}
				_ = tls.Server(c, serverTLS).Handshake()
			}(conn)
		}
	}()
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	return port
}

// TestNewWithDefaults_ServerCertificateNotTrusted_HintsOnlyWithoutCA pins
// that NewWithDefaults adds the sslrootcert hint to a certificate
// failure exactly when the approved configuration carries no CA
// (MTIX-48, MTIX-95.25).
func TestNewWithDefaults_ServerCertificateNotTrusted_HintsOnlyWithoutCA(t *testing.T) {
	port := startUntrustedTLSServer(t)
	otherCA, _ := writeTestCA(t)
	base := "postgres://u:pw@127.0.0.1:" + port + "/hub?connect_timeout=5"
	tests := []struct {
		name     string
		dsn      string
		env      map[string]string
		wantHint bool
	}{
		{"no CA supplied", base, nil, true},
		{"CA named in the DSN", base + "&sslrootcert=" + url.QueryEscape(otherCA), nil, false},
		{"CA from MTIX_SYNC_SSLROOTCERT", base, map[string]string{transport.EnvSSLRootCert: otherCA}, false},
		{"CA from PGSSLROOTCERT", base, map[string]string{"PGSSLROOTCERT": otherCA}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			pool, err := transport.NewWithDefaults(ctx, tt.dsn, transport.Options{}, transport.DefaultPoolDefaults())
			require.Nil(t, pool)
			require.Error(t, err)
			require.Contains(t, err.Error(), "x509:", "the failure is certificate verification")
			if tt.wantHint {
				require.Contains(t, err.Error(), "hint:")
				require.Contains(t, err.Error(), "sslrootcert=<path>")
				return
			}
			require.NotContains(t, err.Error(), "hint:", "no hint once a CA is supplied")
		})
	}
}
