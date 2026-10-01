// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package transport

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
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

// TestHintTLSTrust_NamesNoProviderAndGivesCapabilityFix: the hint states the
// fix as capability wording (a private-CA hub needs an explicit root
// certificate) and names no hosting provider (MTIX-107.38).
func TestHintTLSTrust_NamesNoProviderAndGivesCapabilityFix(t *testing.T) {
	t.Setenv(EnvSSLRootCert, "")
	certErr := errors.New("tls: failed to verify certificate: x509: unknown authority")
	got := hintTLSTrust("postgres://u@host:5432/db?sslmode=verify-full", certErr).Error()
	for _, want := range []string{"private CA", "sslrootcert=<ca.pem>", EnvSSLRootCert} {
		if !strings.Contains(got, want) {
			t.Fatalf("hint missing %q: %s", want, got)
		}
	}
	if providerNameRE.MatchString(got) {
		t.Fatalf("hint names a provider: %s", got)
	}
}

// providerNameRE matches the hosting and database provider names that shipped
// text must not carry (CLAUDE.md provider-neutral rule), on word boundaries.
var providerNameRE = regexp.MustCompile(`(?i)\b(supabase|neon|aurora|rds|cloud sql|alloydb)\b`)

// TestNoProviderNameInShippedGoStringLiterals: no non-test Go string literal
// under cmd/ or internal/ names a hosting or database provider (MTIX-107.38).
func TestNoProviderNameInShippedGoStringLiterals(t *testing.T) {
	root := filepath.Join("..", "..", "..", "..")
	fset := token.NewFileSet()
	scanned := 0
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, perr := parser.ParseFile(fset, path, nil, 0)
			if perr != nil {
				return perr
			}
			scanned++
			ast.Inspect(f, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				val, uerr := strconv.Unquote(lit.Value)
				if uerr != nil {
					val = lit.Value
				}
				if m := providerNameRE.FindString(val); m != "" {
					t.Errorf("%s: string literal names provider %q", fset.Position(lit.Pos()), m)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if scanned < 50 {
		t.Fatalf("scanned only %d files; the walk is not reaching cmd/ and internal/", scanned)
	}
}
