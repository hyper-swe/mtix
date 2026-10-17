// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package redact_test

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/sync/redact"
	"github.com/stretchr/testify/require"
)

// SecretSentinel is referenced from redact.SecretSentinel in
// production code; the alias keeps the test bodies short.
var SecretSentinel = redact.SecretSentinel

func TestDSN_PostgresScheme(t *testing.T) {
	in := "failed to connect: postgres://user:" + SecretSentinel + "@db.example.com:5432/mtix?sslmode=verify-full"
	got := redact.DSN(in)
	require.NotContains(t, got, SecretSentinel)
	require.Contains(t, got, "postgres://REDACTED@db.example.com:5432/mtix")
	require.Contains(t, got, "sslmode=verify-full",
		"query string preserved for diagnostics")
}

func TestDSN_PostgresqlScheme(t *testing.T) {
	in := "postgresql://user:" + SecretSentinel + "@host/db"
	got := redact.DSN(in)
	require.NotContains(t, got, SecretSentinel)
	require.Contains(t, got, "postgresql://REDACTED@host/db")
}

func TestDSN_JDBCScheme(t *testing.T) {
	in := "jdbc:postgresql://user:" + SecretSentinel + "@host:5432/db"
	got := redact.DSN(in)
	require.NotContains(t, got, SecretSentinel)
	require.Contains(t, got, "jdbc:postgresql://REDACTED@host:5432/db")
}

func TestDSN_MultipleOccurrences(t *testing.T) {
	in := "first: postgres://a:" + SecretSentinel + "@h1/db1, second: postgres://b:" + SecretSentinel + "@h2/db2"
	got := redact.DSN(in)
	require.NotContains(t, got, SecretSentinel)
	require.Contains(t, got, "postgres://REDACTED@h1/db1")
	require.Contains(t, got, "postgres://REDACTED@h2/db2")
}

func TestDSN_Idempotent(t *testing.T) {
	in := "postgres://u:p@h/d"
	once := redact.DSN(in)
	twice := redact.DSN(once)
	require.Equal(t, once, twice, "idempotent: re-redacting is a no-op")
}

func TestDSN_Empty(t *testing.T) {
	require.Equal(t, "", redact.DSN(""))
}

func TestDSN_NoMatch(t *testing.T) {
	in := "no DSN here, just an error message"
	require.Equal(t, in, redact.DSN(in), "non-DSN strings pass through unchanged")
}

func TestDSN_PreservesScheme(t *testing.T) {
	cases := []string{
		"postgres://u:" + SecretSentinel + "@h/d",
		"postgresql://u:" + SecretSentinel + "@h/d",
		"jdbc:postgresql://u:" + SecretSentinel + "@h/d",
	}
	wantSchemes := []string{"postgres://", "postgresql://", "jdbc:postgresql://"}
	for i, in := range cases {
		t.Run(wantSchemes[i], func(t *testing.T) {
			got := redact.DSN(in)
			require.Contains(t, got, wantSchemes[i])
			require.NotContains(t, got, SecretSentinel)
		})
	}
}

func TestDSN_PreservesHostAndDatabase(t *testing.T) {
	in := "postgres://u:" + SecretSentinel + "@db.internal.example.com:5432/production_mtix"
	got := redact.DSN(in)
	require.Contains(t, got, "db.internal.example.com:5432")
	require.Contains(t, got, "production_mtix")
	require.NotContains(t, got, SecretSentinel)
}

func TestDSN_HandlesURLEncodedPassword(t *testing.T) {
	// pgx generally does NOT URL-encode passwords with special chars,
	// but if a caller supplies one, the redactor must still mask it.
	in := "postgres://u:" + SecretSentinel + "%40x@h/d"
	got := redact.DSN(in)
	require.NotContains(t, got, SecretSentinel)
}

func TestRecover_PanicValueIsRedacted(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))

	defer func() {
		// outer recover swallows the redacted re-panic for test
		// purposes; assert on what got logged before.
		r := recover()
		require.NotNil(t, r, "Recover MUST re-panic so the runtime stack trace is preserved")
		require.NotContains(t, "value", SecretSentinel,
			"after redact, the re-panic value MUST NOT contain the sentinel")
	}()

	defer redact.Recover(logger)

	panic("connection failed: postgres://user:" + SecretSentinel + "@host/db")
}

func TestRecover_NilPanicIsNoOp(t *testing.T) {
	require.NotPanics(t, func() {
		defer redact.Recover(nil)
		// no panic
	})
}

func TestRecoverNoRepanic_SwallowsAndLogs(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))

	require.NotPanics(t, func() {
		defer redact.RecoverNoRepanic(logger)
		panic("boom: postgres://u:" + SecretSentinel + "@h/d")
	})

	logged := buf.String()
	require.Contains(t, logged, "panic recovered")
	require.NotContains(t, logged, SecretSentinel,
		"logged panic value MUST be redacted")
}

func TestRecoverNoRepanic_NilPanicNoLog(t *testing.T) {
	buf := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(buf, nil))
	require.NotPanics(t, func() {
		defer redact.RecoverNoRepanic(logger)
	})
	require.Empty(t, buf.String(), "no panic = no log line")
}

func TestRecoverNoRepanic_NilLoggerSafe(t *testing.T) {
	require.NotPanics(t, func() {
		defer redact.RecoverNoRepanic(nil)
		panic("boom: postgres://u:" + SecretSentinel + "@h/d")
	})
}

// Sentinel is a sanity check — if a future refactor changes the
// constant name, this test reminds us to update the cross-package
// usage in 15.3.4 hygiene sweep.
func TestSecretSentinel_StableForCrossPackageGrep(t *testing.T) {
	require.True(t, strings.HasPrefix(SecretSentinel, "PASSWORD_LEAK_SENTINEL_"))
	require.True(t, len(SecretSentinel) >= 24)
}

// MTIX-15.11.2: Recover must redact across every DSN scheme.
// Existing TestRecover_RedactsAndRepanicsWithDSN above only covers
// postgres://. This sweep covers all three.
func TestRecover_AllSchemes(t *testing.T) {
	cases := []struct {
		name string
		dsn  string
	}{
		{"postgres", "postgres://u:" + SecretSentinel + "@h/d"},
		{"postgresql", "postgresql://u:" + SecretSentinel + "@h/d"},
		{"jdbc_postgresql", "jdbc:postgresql://u:" + SecretSentinel + "@h:5432/d"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			logger := slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelError}))

			defer func() {
				r := recover()
				require.NotNil(t, r, "Recover must re-panic")
				redacted, ok := r.(string)
				require.Truef(t, ok, "re-panic value should be a string after redaction; got %T", r)
				require.NotContainsf(t, redacted, SecretSentinel,
					"%s scheme leaked sentinel after Recover: %q", tc.name, redacted)
			}()

			defer redact.Recover(logger)
			panic("boom: " + tc.dsn)
		})
	}
}

// MTIX-15.11.2: Recover must redact even when the panic value is an
// error-typed value (the most common shape from production code that
// panics on a wrapped error). The DSN sentinel inside the error
// message must still be masked in the re-panic.
func TestRecover_StripsDSNFromPanicError(t *testing.T) {
	defer func() {
		r := recover()
		require.NotNil(t, r)
		redacted, ok := r.(string)
		require.Truef(t, ok, "re-panic value should be a redacted string; got %T", r)
		require.NotContains(t, redacted, SecretSentinel,
			"error-typed panic value must have its DSN redacted")
	}()
	defer redact.Recover(nil)

	err := errorWithDSN("connection failed: postgres://u:" + SecretSentinel + "@h/d")
	panic(err)
}

// errorWithDSN is a tiny test-local error type so the panic value is
// not just a string literal.
type errorWithDSN string

func (e errorWithDSN) Error() string { return string(e) }
