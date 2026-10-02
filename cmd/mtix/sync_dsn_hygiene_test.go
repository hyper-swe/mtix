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
	"github.com/hyper-swe/mtix/internal/sync/redact"
)

// sweepCommand is one CLI entry point in the FR-18.17 output sweep.
type sweepCommand struct {
	name string
	// argv is the command line under mtix; pos is the positional DSN,
	// if any, placed where a positional argument would go.
	argv func(pos []string) []string
	// reached is output that proves a DSN from the environment or the
	// secrets file reached the command's DSN path. Empty for commands
	// that never read a DSN.
	reached string
	// timeout bounds a long-running command (the daemons).
	timeout time.Duration
	// argsOnly marks a command that never reads a DSN and acts outside
	// the test (OS service registration, the FR-15 file check on the
	// real stdout). The sweep runs it only with a positional argument,
	// which its argument rule refuses before the command runs.
	argsOnly bool
}

// cmdLine joins a command path, positional arguments and flags.
func cmdLine(path []string, pos []string, flags ...string) []string {
	out := append([]string{}, path...)
	out = append(out, pos...)
	return append(out, flags...)
}

// sweepCommands lists every sync command, both daemons (also with
// --install) and the daemon service verbs, except doctor: its cobra
// wrapper exits the process on failed checks, so the sweep runs it
// through sweepDoctor instead.
func sweepCommands(backupOut string) []sweepCommand {
	const daemonTimeout = 500 * time.Millisecond
	const connect = "mtix sync connect:"
	const pullErr = "pull error (will retry)"
	at := func(path ...string) func(pos []string) []string {
		return func(pos []string) []string { return cmdLine(path, pos) }
	}
	return []sweepCommand{
		{name: "sync init", argv: at("sync", "init"), reached: connect},
		{name: "sync clone", argv: at("sync", "clone"), reached: connect},
		{name: "sync push", argv: at("sync", "push"), reached: connect},
		{name: "sync pull", argv: at("sync", "pull"), reached: connect},
		{name: "sync mark-restored", argv: at("sync", "mark-restored"), reached: connect},
		{name: "sync harden", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "harden"}, pos, "--apply", "--keep-role", "mtix_team")
		}, reached: "mtix sync harden connect:"},
		{name: "sync migrate", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "migrate"}, pos, "--project", "TEST")
		}, reached: connect},
		{name: "sync collisions list", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "collisions", "list"}, pos, "--project", "TEST")
		}, reached: connect},
		{name: "sync collisions resolve", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "collisions", "resolve", "1"}, pos, "--winner", "held")
		}, reached: connect},
		{name: "sync backup", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "backup"}, pos, "--output", backupOut)
		}, reached: "mtix sync "},
		{name: "sync daemon", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "daemon"}, pos, "--interval", "3600")
		}, reached: pullErr, timeout: daemonTimeout},
		{name: "daemon", argv: func(pos []string) []string {
			return cmdLine([]string{"daemon"}, pos, "--interval", "3600")
		}, reached: pullErr, timeout: daemonTimeout},
		{name: "sync status", argv: at("sync", "status")},
		{name: "sync conflicts list", argv: at("sync", "conflicts", "list")},
		{name: "sync conflicts resolve", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "conflicts", "resolve", "1"}, pos, "--action", "acknowledge")
		}},
		{name: "sync reconcile", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "reconcile"}, pos, "--discard-local")
		}},
		{name: "sync quarantine list", argv: at("sync", "quarantine", "list")},
		{name: "sync repair", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "repair"}, pos, "--status")
		}},
		{name: "sync repair-uids", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "repair-uids"}, pos, "--dry-run")
		}},
		{name: "sync backfill", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "backfill"}, pos, "--dry-run")
		}},
		{name: "sync", argv: at("sync"), argsOnly: true},
		{name: "sync daemon --install", argv: func(pos []string) []string {
			return cmdLine([]string{"sync", "daemon"}, pos, "--install")
		}},
		{name: "daemon --install", argv: func(pos []string) []string {
			return cmdLine([]string{"daemon"}, pos, "--install")
		}},
		{name: "daemon install", argv: at("daemon", "install"), argsOnly: true},
		{name: "daemon uninstall", argv: at("daemon", "uninstall"), argsOnly: true},
		{name: "daemon start", argv: at("daemon", "start"), argsOnly: true},
		{name: "daemon stop", argv: at("daemon", "stop"), argsOnly: true},
		{name: "daemon status", argv: at("daemon", "status"), argsOnly: true},
	}
}

// sweepDoctor runs mtix sync doctor for the sweep and returns everything
// it made observable. A positional argument meets the command's argument
// rule first, as on the command line; the checks themselves run through
// runSyncDoctor, because the cobra wrapper exits the process when a
// check fails.
func sweepDoctor(pos []string) string {
	if err := newSyncDoctorCmd().ValidateArgs(pos); err != nil {
		return errorsAsText(err)
	}
	var stdout, stderr bytes.Buffer
	err := runSyncDoctor(context.Background(), &stdout, &stderr, pos, transport.Options{})
	return errorsAsText(err, stdout.String(), stderr.String())
}

// execSyncCLI runs argv under a root configured like production's
// (cobra's own error and usage printing silenced, so the returned error
// is the only error text) with the sync and daemon trees attached.
func execSyncCLI(ctx context.Context, argv []string) (string, string, error) {
	root := &cobra.Command{Use: "mtix", SilenceErrors: true, SilenceUsage: true}
	root.AddCommand(newSyncCmd(), newDaemonCmd())
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(argv)
	err := root.ExecuteContext(ctx)
	return stdout.String(), stderr.String(), err
}

// runSweepCommand runs one sweep command with its timeout and returns
// everything it made observable: stdout, stderr and the returned error.
func runSweepCommand(t *testing.T, c sweepCommand, pos []string) string {
	t.Helper()
	timeout := c.timeout
	if timeout == 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	stdout, stderr, err := execSyncCLI(ctx, c.argv(pos))
	return errorsAsText(err, stdout, stderr)
}

// requireDSNPathReached fails when a command stopped before its DSN
// path, which would make the leak check for it vacuous.
func requireDSNPathReached(t *testing.T, label, text, reached string) {
	t.Helper()
	for _, early := range []string{"no DSN configured", "not in an mtix project", "local store not initialized"} {
		require.NotContainsf(t, text, early, "%s stopped before its DSN path:\n%s", label, text)
	}
	require.Containsf(t, text, reached, "%s did not reach its DSN path:\n%s", label, text)
}

// TestDSN_NeverInAnyFR18CommandOutput is the FR-18.17 regression sweep
// (MTIX-15.7.5, extended by MTIX-95.15). Every sync command, both
// daemons and the daemon service verbs run against a real local store
// with a synthetic DSN in each form (well-formed, keyword/value,
// malformed, scheme-less) and from each source (the environment, the
// secrets file, a symlinked secrets file, the command line). No part of the password, and never
// the DSN itself, may appear in stdout, stderr or the returned error.
//
// No hub is reachable (the host is under .invalid), so each command runs
// its failure path. For environment and secrets-file DSNs the sweep also
// checks that each DSN-reading command reached its DSN path, so a
// command that stops early cannot pass vacuously.
func TestDSN_NeverInAnyFR18CommandOutput(t *testing.T) {
	t.Setenv("MTIX_SYNC_HOOK", "")
	t.Setenv("MTIX_PG_DUMP", filepath.Join(t.TempDir(), "absent-pg_dump"))
	for _, d := range syntheticDSNs() {
		for _, source := range []string{sourceEnv, sourceSecrets, sourceSecretsLink, sourcePositional} {
			t.Run(d.name+"/"+source, func(t *testing.T) {
				initTestApp(t)
				pos := configureSyncDSN(t, app.mtixDir, source, d.dsn)
				backupOut := filepath.Join(t.TempDir(), "hub.sql")
				for _, c := range sweepCommands(backupOut) {
					if c.argsOnly && source != sourcePositional {
						continue
					}
					text := runSweepCommand(t, c, pos)
					requireNoSecret(t, c.name, text, d)
					if source != sourcePositional && c.reached != "" {
						requireDSNPathReached(t, c.name, text, c.reached)
					}
				}

				text := sweepDoctor(pos)
				requireNoSecret(t, "sync doctor", text, d)
				if source != sourcePositional {
					requireDSNPathReached(t, "sync doctor", text, "PG reachable")
				}
			})
		}
	}
}

// TestDSNSweep_CoversEverySyncCommand: every runnable command under
// mtix sync and mtix daemon, the two parents included, is in the output
// sweep, so a new command cannot ship outside it (FR-18.17, MTIX-95.15).
func TestDSNSweep_CoversEverySyncCommand(t *testing.T) {
	swept := map[string]bool{"sync doctor": true}
	for _, c := range sweepCommands("") {
		swept[c.name] = true
	}
	root := &cobra.Command{Use: "mtix"}
	root.AddCommand(newSyncCmd(), newDaemonCmd())
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		path := strings.TrimPrefix(c.CommandPath(), "mtix ")
		if c == root || !c.Runnable() {
			return // the test root and command groups without a RunE
		}
		// The FR-21 file relay is a directory transport: no relay command
		// reads or accepts a hub DSN, and its positional argument is a
		// path, so a DSN-shaped argument would name a directory.
		if strings.HasPrefix(path, "sync relay ") {
			return
		}
		require.Truef(t, swept[path], "%q is missing from the FR-18.17 output sweep", path)
	}
	walk(root)
}

func TestDSN_RedactDSNCatchesAllSchemes(t *testing.T) {
	// Sanity that the redact package does what the sweep relies on.
	cases := []struct {
		scheme string
		dsn    string
	}{
		{"postgres://", "postgres://user:" + redact.SecretSentinel + "@host/db"},
		{"postgresql://", "postgresql://user:" + redact.SecretSentinel + "@host/db"},
		{"jdbc:postgresql://", "jdbc:postgresql://user:" + redact.SecretSentinel + "@host/db"},
	}
	for _, tc := range cases {
		t.Run(tc.scheme, func(t *testing.T) {
			redacted := redact.DSN(tc.dsn)
			require.NotContainsf(t, redacted, redact.SecretSentinel,
				"%s scheme leaked the sentinel after redaction", tc.scheme)
			// The redacted form should still contain the host so
			// operators can debug — verify via the host substring
			// derived from the DSN.
			require.True(t, strings.Contains(redacted, "host/db") || strings.Contains(redacted, "@host"),
				"redaction should preserve host: got %q", redacted)
		})
	}
}
