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
