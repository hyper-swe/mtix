// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

func TestRunDaemon_TLSHostnameMismatch_PrintsAddressAndHubRecovery(t *testing.T) {
	src := daemonOutcomeFixture(t)
	dsn, calls := hostnameMismatchHub(t)
	t.Setenv(transport.EnvDSN, dsn)
	_, stderr := runOutcomeDaemonOnce(t)
	require.Positive(t, calls.Load())
	require.Contains(t, stderr, "certificate")
	row := hubContactRow(src, true, src.now())
	require.False(t, row.Pass)
	require.Contains(t, row.Detail, "tls")
	require.Contains(t, row.Fix, "DSN host matches the certificate")
	require.Contains(t, row.Fix, "hub operator")
	require.Contains(t, row.Fix, "renew")
	require.Contains(t, row.Fix, "enable TLS")
}

func hostnameMismatchHub(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	cert := &x509.Certificate{SerialNumber: big.NewInt(32), NotBefore: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), NotAfter: time.Date(2036, 1, 1, 0, 0, 0, 0, time.UTC), DNSNames: []string{"correct.invalid"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	require.NoError(t, err)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
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
			var startup [8]byte
			if _, err := io.ReadFull(conn, startup[:]); err == nil {
				_, _ = conn.Write([]byte{'S'})
				peer := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
				_ = peer.Handshake()
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { require.NoError(t, ln.Close()); <-done })
	return "postgres://user:hostname-secret@" + ln.Addr().String() + "/board?sslmode=verify-full&sslrootcert=" + url.QueryEscape(ca), calls
}
