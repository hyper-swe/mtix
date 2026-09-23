// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// positionalRefusal is the fixed message for a DSN on the command line
// (FR-18.16, MTIX-95.15).
const positionalRefusal = "a DSN on the command line is refused: set MTIX_SYNC_DSN or .mtix/secrets"

// TestSyncCommands_PositionalDSN_Refused: every sync command, mtix sync
// daemon and mtix daemon refuse a DSN given as a positional argument
// with one fixed message that names MTIX_SYNC_DSN and .mtix/secrets and
// never repeats the argument (FR-18.16, MTIX-95.15). A hub DSN is also
// configured in the environment, so the refusal cannot be mistaken for
// the "no DSN configured" error, which names the same two sources.
func TestSyncCommands_PositionalDSN_Refused(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "")
	positional := []syntheticDSN{wellFormedDSN(), malformedDSNs()[0], malformedDSNs()[3]}
	for _, d := range positional {
		t.Run(d.name, func(t *testing.T) {
			initTestApp(t)
			t.Setenv(transport.EnvDSN, "postgres://envuser@127.0.0.1:1/mtix?sslmode=verify-full&connect_timeout=1")
			backupOut := filepath.Join(t.TempDir(), "hub.sql")
			for _, c := range sweepCommands(backupOut) {
				t.Run(c.name, func(t *testing.T) {
					// A daemon that wrongly starts its loop still returns
					// when this deadline passes, so the test cannot hang.
					timeout := 30 * time.Second
					if c.timeout > 0 {
						timeout = 2 * c.timeout
					}
					ctx, cancel := context.WithTimeout(context.Background(), timeout)
					defer cancel()
					stdout, stderr, err := execSyncCLI(ctx, c.argv([]string{d.dsn}))
					require.Error(t, err, "a DSN on the command line is refused")
					require.Contains(t, err.Error(), positionalRefusal)
					requireNoSecret(t, c.name, errorsAsText(err, stdout, stderr), d)
					require.NotContains(t, stdout, "started", "a refused daemon never starts")
				})
			}

			var stdout, stderr bytes.Buffer
			err := runSyncDoctor(context.Background(), &stdout, &stderr,
				[]string{d.dsn}, transport.Options{})
			require.ErrorIs(t, err, errDoctorChecksFailed)
			require.Contains(t, stdout.String(), positionalRefusal,
				"doctor reports the refusal in its PG check")
			requireNoSecret(t, "sync doctor", errorsAsText(err, stdout.String(), stderr.String()), d)
		})
	}
}

// TestResolveSyncDSN_PositionalArg_Refused: resolveSyncDSN refuses any
// positional argument with the fixed message and returns no DSN, even
// when the environment provides one (FR-18.16, MTIX-95.15).
func TestResolveSyncDSN_PositionalArg_Refused(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = t.TempDir()
	t.Cleanup(func() { app.mtixDir = saved })
	t.Setenv(transport.EnvDSN, "postgres://env@host/db")

	for _, args := range [][]string{
		{"postgres://positional:" + sweepSecret + "@host/db"},
		{"positional:" + sweepSecret + "@host/db"},
		{""},
	} {
		got, err := resolveSyncDSN(args)
		require.ErrorIs(t, err, transport.ErrPositionalDSN)
		require.Equal(t, positionalRefusal, err.Error())
		require.Empty(t, got)
	}
}

// TestSyncCommands_UseStrings_OfferNoPositionalDSN: no usage line under
// mtix sync or mtix daemon offers a DSN argument (FR-18.16, MTIX-95.15).
func TestSyncCommands_UseStrings_OfferNoPositionalDSN(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		require.NotContainsf(t, strings.ToUpper(c.Use), "DSN",
			"%q offers a positional DSN in its usage %q", c.CommandPath(), c.Use)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(newSyncCmd())
	walk(newDaemonCmd())
}

// TestSyncPush_PositionalDSNInHookMode_StillRefused: hook mode
// (MTIX_SYNC_HOOK=1) degrades only transient hub errors; a DSN on the
// command line is still refused, so a hook cannot silently skip every
// push (FR-18.16, FR-18.19, MTIX-95.15).
func TestSyncPush_PositionalDSNInHookMode_StillRefused(t *testing.T) {
	initTestApp(t)
	t.Setenv("MTIX_SYNC_HOOK", "1")
	t.Setenv(transport.EnvDSN, "")
	d := wellFormedDSN()
	stdout, stderr, err := execSyncCLI(context.Background(), []string{"sync", "push", d.dsn})
	require.Error(t, err)
	require.Contains(t, err.Error(), positionalRefusal)
	requireNoSecret(t, "hook-mode push", errorsAsText(err, stdout, stderr), d)
}

// TestSyncExactArgs_MissingArgument_CountOnly: a sync command missing
// its documented argument reports only the count, and one with an
// extra argument gives the fixed refusal wrapping ErrPositionalDSN
// (FR-18.16, MTIX-95.15).
func TestSyncExactArgs_MissingArgument_CountOnly(t *testing.T) {
	cmd := &cobra.Command{Use: "resolve"}
	require.NoError(t, syncExactArgs(1)(cmd, []string{"7"}))

	err := syncExactArgs(1)(cmd, nil)
	require.EqualError(t, err, "resolve: requires 1 argument(s), received 0")

	err = syncExactArgs(1)(cmd, []string{"7", "sweepuser:" + sweepSecret + "@" + sweepHost})
	require.ErrorIs(t, err, transport.ErrPositionalDSN)
	require.EqualError(t, err, "resolve: "+positionalRefusal)

	err = syncExactArgs(0)(cmd, []string{"x"})
	require.ErrorIs(t, err, transport.ErrPositionalDSN)
	require.NoError(t, syncExactArgs(0)(cmd, nil))
}
