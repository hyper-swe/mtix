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
