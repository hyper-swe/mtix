// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"errors"
	"strings"
	"testing"
)

// TestHintTLSTrust: a verify-full failure with no CA supplied gets actionable
// sslrootcert guidance (MTIX-48); other errors and CA-already-supplied cases
// pass through unchanged.
func TestHintTLSTrust(t *testing.T) {
	certErr := errors.New(`initial ping: failed to write startup message: ` +
		`write failed: tls: failed to verify certificate: x509: ` +
		`"*.pooler.supabase.com" certificate is not standards compliant`)
	nonCertErr := errors.New("initial ping: dial tcp: connection refused")

	t.Run("cert failure, no CA -> hint added, original wrapped", func(t *testing.T) {
		t.Setenv(EnvSSLRootCert, "")
		got := hintTLSTrust("postgres://u@host:5432/db?sslmode=verify-full", certErr)
		if !strings.Contains(got.Error(), "sslrootcert") {
			t.Fatalf("expected sslrootcert guidance, got: %v", got)
		}
		if !errors.Is(got, certErr) {
			t.Fatal("must wrap (errors.Is) the original error")
		}
	})

	t.Run("cert failure but CA already in DSN -> passthrough", func(t *testing.T) {
		t.Setenv(EnvSSLRootCert, "")
		got := hintTLSTrust("postgres://u@host/db?sslmode=verify-full&sslrootcert=/ca.pem", certErr)
		if strings.Contains(got.Error(), "hint:") {
			t.Fatalf("must not hint when sslrootcert already set: %v", got)
		}
	})

	t.Run("non-cert error -> passthrough", func(t *testing.T) {
		t.Setenv(EnvSSLRootCert, "")
		got := hintTLSTrust("postgres://u@host/db", nonCertErr)
		if strings.Contains(got.Error(), "hint:") {
			t.Fatalf("must not hint on a non-cert error: %v", got)
		}
	})

	t.Run("nil error -> nil", func(t *testing.T) {
		if hintTLSTrust("postgres://u@host/db", nil) != nil {
			t.Fatal("nil in must stay nil out")
		}
	})
}

// TestIsRetryableConnErr: only transient network symptoms retry; TLS and auth
// failures fail fast (MTIX-48.3).
func TestIsRetryableConnErr(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		retry bool
	}{
		{"nil", nil, false},
		{"connection refused", errors.New("failed to connect to `host`: dial tcp 1.2.3.4:5432: connect: connection refused"), true},
		{"connection reset", errors.New("read tcp: connection reset by peer"), true},
		{"i/o timeout", errors.New("dial tcp: i/o timeout"), true},
		{"server closed", errors.New("unexpected EOF: server closed the connection unexpectedly"), true},
		{"cert failure not retried", errors.New("failed to connect: tls: failed to verify certificate: x509: not standards compliant"), false},
		{"auth failure not retried", errors.New("failed to connect: FATAL: password authentication failed for user"), false},
		{"unknown db not retried", errors.New(`database "x" does not exist (SQLSTATE 3D000)`), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableConnErr(tc.err); got != tc.retry {
				t.Fatalf("isRetryableConnErr(%q) = %v, want %v", tc.err, got, tc.retry)
			}
		})
	}
}
