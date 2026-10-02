// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport_test

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// sslRequestMessage is the 8-byte SSLRequest a client sends before a TLS
// handshake: length 8, then the request code 80877103.
const sslRequestMessage = "\x00\x00\x00\x08\x04\xd2\x16\x2f"

// untrustedTLSServer listens on loopback, answers each connection's
// SSLRequest with 'S' and then offers a self-signed certificate that no
// test CA signed. It counts the connections it accepts, the TLS
// handshakes it starts and the ones that complete; a client that
// verifies the certificate never completes one. A connection that opens
// with anything but an SSLRequest (a plaintext attempt) and any other
// server-side failure are recorded.
type untrustedTLSServer struct {
	port      string
	ln        net.Listener
	tlsConfig *tls.Config
	wg        sync.WaitGroup
	stopOnce  sync.Once
	stopErr   error
	accepted  atomic.Int64
	started   atomic.Int64
	completed atomic.Int64
	mu        sync.Mutex
	failures  []string
}

// startUntrustedTLSServer starts an untrustedTLSServer; the test's
// cleanup stops it.
func startUntrustedTLSServer(t *testing.T) *untrustedTLSServer {
	t.Helper()
	certPEM, keyPEM := selfSignedPEM(t, "untrusted test server")
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	require.NoError(t, err)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	s := &untrustedTLSServer{port: port, ln: ln,
		tlsConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}}
	s.wg.Add(1)
	go s.serve()
	t.Cleanup(func() { require.NoError(t, s.stop()) })
	return s
}

// serve accepts connections until stop closes the listener.
func (s *untrustedTLSServer) serve() {
	defer s.wg.Done()
	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				s.fail("accept", err)
			}
			return
		}
		s.accepted.Add(1)
		s.wg.Add(1)
		go s.handle(conn)
	}
}

// handle answers one connection's SSLRequest and runs the server side
// of the TLS handshake.
func (s *untrustedTLSServer) handle(c net.Conn) {
	defer s.wg.Done()
	// Deliberately ignored: the client has usually dropped the
	// connection already, and no assertion depends on Close.
	defer func() { _ = c.Close() }()
	if err := c.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
		s.fail("set deadline", err)
		return
	}
	sslRequest := make([]byte, len(sslRequestMessage))
	if _, err := io.ReadFull(c, sslRequest); err != nil {
		s.fail("read SSLRequest", err)
		return
	}
	if string(sslRequest) != sslRequestMessage {
		s.fail("first message", errors.New("not an SSLRequest: the attempt did not use TLS"))
		return
	}
	if _, err := c.Write([]byte{'S'}); err != nil {
		s.fail("answer SSLRequest", err)
		return
	}
	s.started.Add(1)
	// A handshake error is the expected outcome: the client refuses the
	// certificate. Only a completed handshake is counted.
	if err := tls.Server(c, s.tlsConfig).Handshake(); err == nil {
		s.completed.Add(1)
	}
}

// fail records a server-side failure other than a refused handshake.
func (s *untrustedTLSServer) fail(step string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures = append(s.failures, step+": "+err.Error())
}

// stop closes the listener and waits for every handler to finish. It is
// safe to call more than once and returns the listener's close error.
func (s *untrustedTLSServer) stop() error {
	s.stopOnce.Do(func() {
		s.stopErr = s.ln.Close()
		s.wg.Wait()
	})
	return s.stopErr
}

// requireHandshakes stops s and checks each connection attempt: it
// accepted want connections, every one opened with an SSLRequest and
// started a TLS handshake, none completed one, and nothing else failed.
func (s *untrustedTLSServer) requireHandshakes(t *testing.T, want int64) {
	t.Helper()
	require.NoError(t, s.stop())
	s.mu.Lock()
	defer s.mu.Unlock()
	require.Empty(t, s.failures, "server-side failures")
	require.Equal(t, want, s.accepted.Load(), "connection attempts")
	require.Equal(t, want, s.started.Load(), "attempts that started a TLS handshake")
	require.Zero(t, s.completed.Load(), "a handshake completed: a connection attempt did not verify the certificate")
}

// TestNewWithDefaults_ServerCertificateNotTrusted_HintsOnlyWithoutCA pins
// that NewWithDefaults adds the sslrootcert hint to a certificate
// failure exactly when the approved configuration carries no CA
// (MTIX-48, MTIX-95.25).
func TestNewWithDefaults_ServerCertificateNotTrusted_HintsOnlyWithoutCA(t *testing.T) {
	otherCA, _ := writeTestCA(t)
	tests := []struct {
		name     string
		query    string
		env      map[string]string
		wantHint bool
	}{
		{"no CA supplied", noDefaultCA, nil, true},
		{"CA named in the DSN", "sslrootcert=" + url.QueryEscape(otherCA), nil, false},
		{"CA from MTIX_SYNC_SSLROOTCERT", "", map[string]string{transport.EnvSSLRootCert: otherCA}, false},
		{"CA from PGSSLROOTCERT", "", map[string]string{"PGSSLROOTCERT": otherCA}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			srv := startUntrustedTLSServer(t)
			dsn := "postgres://u:pw@127.0.0.1:" + srv.port + "/hub?connect_timeout=5"
			if tt.query != "" {
				dsn = withParam(dsn, tt.query)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			pool, err := transport.NewWithDefaults(ctx, dsn, transport.Options{}, transport.DefaultPoolDefaults())
			require.Nil(t, pool)
			require.Error(t, err)
			require.Contains(t, err.Error(), "x509:", "the failure is certificate verification")
			srv.requireHandshakes(t, 1)
			if tt.wantHint {
				require.Contains(t, err.Error(), "hint:")
				require.Contains(t, err.Error(), "sslrootcert=<ca.pem>")
				return
			}
			require.NotContains(t, err.Error(), "hint:", "no hint once a CA is supplied")
		})
	}
}

// TestNewWithDefaults_VerifyFullHostList_EveryAttemptVerifiesCertificate
// pins, through NewWithDefaults itself, that every host entry of the
// pool it opens verifies the server certificate under verify-full, the
// fallbacks included: the untrusted server is listed twice, both
// attempts reach it, and neither completes a TLS handshake (FR-18.15,
// MTIX-95.25).
func TestNewWithDefaults_VerifyFullHostList_EveryAttemptVerifiesCertificate(t *testing.T) {
	tests := []struct {
		name string
		dsn  func(port string) string
	}{
		{"authority host list", func(p string) string {
			return "postgres://u:pw@127.0.0.1:" + p + ",127.0.0.1:" + p + "/hub?connect_timeout=5&" + noDefaultCA
		}},
		{"host parameter list", func(p string) string {
			return "postgres://u:pw@/hub?host=127.0.0.1,127.0.0.1&port=" + p + "," + p + "&connect_timeout=5&" + noDefaultCA
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pinPGEnv(t)
			srv := startUntrustedTLSServer(t)
			ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
			defer cancel()
			pool, err := transport.NewWithDefaults(ctx, tt.dsn(srv.port), transport.Options{}, transport.DefaultPoolDefaults())
			require.Nil(t, pool)
			require.Error(t, err)
			require.Contains(t, err.Error(), "x509:", "the failure is certificate verification")
			srv.requireHandshakes(t, 2)
		})
	}
}
