// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// PG-free CLI tests for mtix sync init / clone. Integration tests
// requiring a real Postgres instance live in cli_integration_test.go
// and skip when MTIX_PG_TEST_DSN is unset.

func TestSyncInitCmd_Construction(t *testing.T) {
	cmd := newSyncInitCmd()
	require.Equal(t, "init [DSN]", cmd.Use)
	require.NotEmpty(t, cmd.Long)
	require.NotNil(t, cmd.Flags().Lookup("insecure-tls"))
}

func TestSyncCloneCmd_Construction(t *testing.T) {
	cmd := newSyncCloneCmd()
	require.Equal(t, "clone [DSN]", cmd.Use)
	require.NotEmpty(t, cmd.Long)
	require.NotNil(t, cmd.Flags().Lookup("insecure-tls"))
	require.NotNil(t, cmd.Flags().Lookup("resume"))
	require.NotNil(t, cmd.Flags().Lookup("batch-size"))
}

func TestSyncCmd_RegistersBothFR18Subcommands(t *testing.T) {
	cmd := newSyncCmd()
	subs := map[string]bool{}
	for _, c := range cmd.Commands() {
		subs[c.Name()] = true
	}
	require.True(t, subs["init"], "sync init subcommand registered")
	require.True(t, subs["clone"], "sync clone subcommand registered")
}

func TestRunSyncInit_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncInit(context.Background(), &stdout, &stderr,
		[]string{"postgres://user:pass@localhost/db?sslmode=disable"},
		transport.Options{InsecureTLS: true})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestRunSyncClone_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncClone(context.Background(), &stdout, &stderr,
		[]string{"postgres://user:pass@localhost/db?sslmode=disable"},
		transport.Options{InsecureTLS: true}, false, 1000)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestResolveSyncDSN_PrefersPositional(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = t.TempDir()
	t.Cleanup(func() { app.mtixDir = saved })

	// Even with no env or secrets file, positional arg wins.
	got, err := resolveSyncDSN([]string{"postgres://positional@host/db"})
	require.NoError(t, err)
	require.Equal(t, "postgres://positional@host/db", got)
}

func TestResolveSyncDSN_FallsBackToTransportSource(t *testing.T) {
	saved := app.mtixDir
	dir := t.TempDir()
	app.mtixDir = dir
	t.Cleanup(func() { app.mtixDir = saved })
	t.Setenv("MTIX_SYNC_DSN", "postgres://env@host/db")

	got, err := resolveSyncDSN(nil)
	require.NoError(t, err)
	require.Equal(t, "postgres://env@host/db", got)
}

func TestWrapSyncErr_HookModeWarnsOnTransient(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "1")
	var stderr bytes.Buffer
	err := wrapSyncErr(&stderr, "connect", errors.New("connection refused: db.example.com:5432"))
	require.NoError(t, err, "hook mode swallows transient errors")
	out := stderr.String()
	require.Contains(t, out, "WARN")
	require.Contains(t, out, "MTIX_SYNC_HOOK=1")
}

func TestWrapSyncErr_HookModeStillFailsOnPermanent(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "1")
	var stderr bytes.Buffer
	err := wrapSyncErr(&stderr, "dsn", transport.ErrDSNNotConfigured)
	require.Error(t, err, "permanent errors must not be swallowed even in hook mode")
	require.Empty(t, stderr.String(), "no WARN line on permanent error")
}

func TestWrapSyncErr_NormalModeAlwaysReturnsError(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "")
	var stderr bytes.Buffer
	err := wrapSyncErr(&stderr, "connect", errors.New("connection refused"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "mtix sync connect:")
}

func TestWrapSyncErr_RedactsDSNInMessage(t *testing.T) {
	var stderr bytes.Buffer
	leaky := errors.New("dial postgres://user:PASSWORD_LEAK_SENTINEL_xyz123@host/db: timeout")
	err := wrapSyncErr(&stderr, "connect", leaky)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "PASSWORD_LEAK_SENTINEL_xyz123",
		"DSN must be redacted in CLI error output")
}

func TestIsTransientSyncErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("connection refused"), true},
		{errors.New("connection reset by peer"), true}, // mirrors transport/retry.go
		{errors.New("no route to host"), true},
		{errors.New("i/o timeout"), true},
		{errors.New("TLS handshake timeout"), true},
		{errors.New("BROKEN PIPE"), true},
		{errors.New("read tcp 1.2.3.4:5432: unexpected EOF"), true},
		{transport.ErrDSNNotConfigured, false},
		{transport.ErrSecretsFileMode, false},
		{transport.ErrDSNInTrackedFile, false},
		{transport.ErrTLSWeakNonLoopback, false},
		{transport.ErrTLSWeakWithoutFlag, false},
		{errors.New("invalid input: foo"), false}, // generic permanent
	}
	for _, tc := range cases {
		t.Run(strings.ReplaceAll(safeErrName(tc.err), " ", "_"), func(t *testing.T) {
			require.Equal(t, tc.want, isTransientSyncErr(tc.err))
		})
	}
}

func safeErrName(err error) string {
	if err == nil {
		return "nil"
	}
	return err.Error()
}

func TestShortHashForCLI(t *testing.T) {
	require.Equal(t, "abc", shortHashForCLI("abc"))
	require.Equal(t, "abcdefghijkl", shortHashForCLI("abcdefghijklmnopqrstuvwxyz"))
}

func TestLowerASCII(t *testing.T) {
	require.Equal(t, "hello", lowerASCII("HELLO"))
	require.Equal(t, "hello123", lowerASCII("HeLLo123"))
}

func TestContainsCI(t *testing.T) {
	require.True(t, containsCI("Connection Refused", "connection"))
	require.True(t, containsCI("BROKEN pipe", "broken pipe"))
	require.False(t, containsCI("ok", "missing"))
}
