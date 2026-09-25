// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

//go:build !windows

package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/migrations"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

// These tests run mtix sync backup against a stand-in pg_dump that records
// its arguments and the PG* environment it received, then writes a small
// dump. No hub is contacted (MTIX-95.7).

// fakePgDumpScript records argv and the PG* environment under
// $MTIX_TEST_PGDUMP_REC, writes the dump to -f's path when given and to
// stdout otherwise, then waits $MTIX_TEST_PGDUMP_SLEEP seconds when set,
// and exits 1 when $MTIX_TEST_PGDUMP_FAIL is set.
const fakePgDumpScript = `#!/bin/sh
rec="$MTIX_TEST_PGDUMP_REC"
: > "$rec/ran"
: > "$rec/argv"
for a in "$@"; do printf '%s\n' "$a" >> "$rec/argv"; done
env | grep '^PG' > "$rec/env"
out=""
while [ $# -gt 0 ]; do
  if [ "$1" = "-f" ]; then out="$2"; shift; fi
  shift
done
if [ -n "$out" ]; then
  printf -- '-- fake dump\n' > "$out"
else
  printf -- '-- fake dump\n'
fi
if [ -n "$MTIX_TEST_PGDUMP_SLEEP" ]; then exec sleep "$MTIX_TEST_PGDUMP_SLEEP"; fi
if [ -n "$MTIX_TEST_PGDUMP_FAIL" ]; then exit 1; fi
exit 0
`

// backupTestPassword is the password of every DSN in these tests.
const backupTestPassword = "Zq7backupSecret"

// fakePgDump is the recording directory of an installed stand-in pg_dump.
type fakePgDump struct{ rec string }

// installFakePgDump points MTIX_PG_DUMP at the stand-in and isolates the
// environment each case controls: HOME (no ~/.postgresql), the PG*
// variables and the mtix sync variables.
func installFakePgDump(t *testing.T) fakePgDump {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "pg_dump")
	require.NoError(t, os.WriteFile(bin, []byte(fakePgDumpScript), 0o700)) //nolint:gosec // test stub must be executable
	rec := filepath.Join(dir, "rec")
	require.NoError(t, os.Mkdir(rec, 0o700))
	t.Setenv("MTIX_PG_DUMP", bin)
	t.Setenv("MTIX_TEST_PGDUMP_REC", rec)
	t.Setenv("MTIX_TEST_PGDUMP_FAIL", "")
	t.Setenv("MTIX_TEST_PGDUMP_SLEEP", "")
	t.Setenv("MTIX_SYNC_HOOK", "")
	t.Setenv("HOME", t.TempDir())
	for _, k := range []string{
		"PGHOST", "PGHOSTADDR", "PGPORT", "PGUSER", "PGPASSWORD", "PGDATABASE",
		"PGSSLMODE", "PGSSLROOTCERT", "PGSERVICE", "PGSERVICEFILE", "PGTARGETSESSIONATTRS",
		transport.EnvSSLRootCert,
	} {
		t.Setenv(k, "")
	}
	return fakePgDump{rec: rec}
}

// ran reports whether the stand-in was executed.
func (f fakePgDump) ran() bool {
	_, err := os.Stat(filepath.Join(f.rec, "ran"))
	return err == nil
}

// argv returns the arguments the stand-in received.
func (f fakePgDump) argv(t *testing.T) []string {
	t.Helper()
	return readLines(t, filepath.Join(f.rec, "argv"))
}

// env returns the non-empty PG* variables the stand-in received.
func (f fakePgDump) env(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, line := range readLines(t, filepath.Join(f.rec, "env")) {
		k, v, ok := strings.Cut(line, "=")
		if ok && v != "" {
			out[k] = v
		}
	}
	return out
}

// readLines returns the lines of path.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test recording path
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()
	var lines []string
	s := bufio.NewScanner(f)
	for s.Scan() {
		lines = append(lines, s.Text())
	}
	require.NoError(t, s.Err())
	return lines
}

// runBackupCLI runs mtix sync backup --output out with the extra flags
// and the hub DSN dsn.
func runBackupCLI(t *testing.T, dsn, out string, flags ...string) (string, string, error) {
	t.Helper()
	t.Setenv(transport.EnvDSN, dsn)
	argv := append([]string{"sync", "backup", "--output", out}, flags...)
	return execSyncCLI(context.Background(), argv)
}

// TestBackup_HonoursTLSPosture: the backup's pg_dump connection uses the
// settings the sync transport approved (transport.ApproveDSN): the same
// sslmode rule (verify-full by default, a weaker mode only with
// --insecure-tls and only on loopback or a local socket), every host and
// port, the CA and target_session_attrs. Service settings in the
// environment do not reach pg_dump, and the password is never on its
// command line (FR-18.15, MTIX-95.7).
func TestBackup_HonoursTLSPosture(t *testing.T) {
	const remote = "postgres://backup_user:" + backupTestPassword + "@db.example.invalid:5432/hub"
	tests := []struct {
		name    string
		dsn     string
		flags   []string
		env     map[string]string
		wantErr string
		wantEnv map[string]string
		absent  []string
	}{
		{name: "a DSN without sslmode is verify-full, as for sync", dsn: remote,
			wantEnv: map[string]string{"PGSSLMODE": "verify-full", "PGHOST": "db.example.invalid", "PGPORT": "5432",
				"PGUSER": "backup_user", "PGDATABASE": "hub", "PGPASSWORD": backupTestPassword, "PGSSLROOTCERT": "system"}},
		{name: "a weaker sslmode needs --insecure-tls", dsn: remote + "?sslmode=require",
			wantErr: "requires --insecure-tls"},
		{name: "--insecure-tls allows a weaker sslmode only on loopback", dsn: remote + "?sslmode=disable",
			flags: []string{"--insecure-tls"}, wantErr: "loopback"},
		{name: "--insecure-tls with a loopback host",
			dsn:   "postgres://backup_user:" + backupTestPassword + "@127.0.0.1:55432/hub?sslmode=disable",
			flags: []string{"--insecure-tls"}, wantEnv: map[string]string{"PGSSLMODE": "disable", "PGHOST": "127.0.0.1", "PGPORT": "55432"},
			absent: []string{"PGSSLROOTCERT"}},
		{name: "every host of a host list",
			dsn:     "postgres://backup_user:" + backupTestPassword + "@h1.example.invalid:5432,h2.example.invalid:6543/hub",
			wantEnv: map[string]string{"PGHOST": "h1.example.invalid,h2.example.invalid", "PGPORT": "5432,6543", "PGSSLMODE": "verify-full"}},
		{name: "a host named in the query string", dsn: remote + "?host=h3.example.invalid",
			wantEnv: map[string]string{"PGHOST": "h3.example.invalid", "PGSSLMODE": "verify-full"}},
		{name: "the CA named in the DSN", dsn: remote + "?sslrootcert={ca}",
			wantEnv: map[string]string{"PGSSLROOTCERT": "{ca}", "PGSSLMODE": "verify-full"}},
		{name: "the CA from MTIX_SYNC_SSLROOTCERT", dsn: remote, env: map[string]string{transport.EnvSSLRootCert: "{ca}"},
			wantEnv: map[string]string{"PGSSLROOTCERT": "{ca}", "PGSSLMODE": "verify-full"}},
		{name: "target_session_attrs", dsn: remote + "?target_session_attrs=read-write",
			wantEnv: map[string]string{"PGTARGETSESSIONATTRS": "read-write"}},
		{name: "service settings in the environment do not reach pg_dump", dsn: remote,
			env:     map[string]string{"PGSERVICEFILE": "{service}", "PGSERVICE": "hub", "PGHOSTADDR": "192.0.2.10"},
			wantEnv: map[string]string{"PGHOST": "db.example.invalid", "PGSSLMODE": "verify-full"},
			absent:  []string{"PGSERVICE", "PGSERVICEFILE", "PGHOSTADDR"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			fake := installFakePgDump(t)
			ca := writeBackupTestCA(t)
			service := filepath.Join(t.TempDir(), "pg_service.conf")
			require.NoError(t, os.WriteFile(service,
				[]byte("[hub]\nhost=other.example.invalid\nsslmode=disable\n"), 0o600))
			fill := strings.NewReplacer("{ca}", ca, "{service}", service)
			for k, v := range tt.env {
				t.Setenv(k, fill.Replace(v))
			}
			out := filepath.Join(t.TempDir(), "hub.sql")

			stdout, stderr, err := runBackupCLI(t, fill.Replace(tt.dsn), out, tt.flags...)
			if tt.wantErr != "" {
				require.Error(t, err)
				require.Contains(t, err.Error(), tt.wantErr)
				require.False(t, fake.ran(), "pg_dump must not run for a refused connection")
				require.NoFileExists(t, out)
				return
			}
			require.NoError(t, err, stderr)
			require.Contains(t, stdout, "backup written to")
			got := fake.env(t)
			for k, v := range tt.wantEnv {
				require.Equalf(t, fill.Replace(v), got[k], "%s passed to pg_dump", k)
			}
			for _, k := range tt.absent {
				require.NotContainsf(t, got, k, "%s must not reach pg_dump", k)
			}
			for _, arg := range fake.argv(t) {
				require.NotContains(t, arg, backupTestPassword, "the password is never on pg_dump's command line")
				require.NotContains(t, arg, "postgres://", "the DSN is never on pg_dump's command line")
			}
		})
	}
}

// TestBackup_OutputIs0600: mtix creates the output file itself, mode 0600
// whatever the umask, before pg_dump writes to it; an existing path (a
// file or a symlink) is refused and left as it was; and a failed dump
// leaves no file behind (MTIX-95.7).
func TestBackup_OutputIs0600(t *testing.T) {
	const dsn = "postgres://backup_user:" + backupTestPassword + "@db.example.invalid:5432/hub"
	oldMask := syscall.Umask(0)
	t.Cleanup(func() { syscall.Umask(oldMask) })

	t.Run("a new path is created 0600 and holds the dump", func(t *testing.T) {
		initTestApp(t)
		fake := installFakePgDump(t)
		out := filepath.Join(t.TempDir(), "hub.sql")
		stdout, stderr, err := runBackupCLI(t, dsn, out)
		require.NoError(t, err, stderr)
		require.True(t, fake.ran())
		info, err := os.Stat(out)
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
		body, err := os.ReadFile(out) //nolint:gosec // path from t.TempDir()
		require.NoError(t, err)
		require.Equal(t, "-- fake dump\n", string(body))
		require.Contains(t, stdout, "backup written to "+out)
	})

	t.Run("an existing file is refused and kept", func(t *testing.T) {
		initTestApp(t)
		fake := installFakePgDump(t)
		out := filepath.Join(t.TempDir(), "hub.sql")
		require.NoError(t, os.WriteFile(out, []byte("keep\n"), 0o640))
		_, _, err := runBackupCLI(t, dsn, out)
		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists")
		require.False(t, fake.ran(), "pg_dump must not run")
		body, readErr := os.ReadFile(out) //nolint:gosec // path from t.TempDir()
		require.NoError(t, readErr)
		require.Equal(t, "keep\n", string(body))
	})

	t.Run("an existing symlink is refused and its target is not created", func(t *testing.T) {
		initTestApp(t)
		fake := installFakePgDump(t)
		dir := t.TempDir()
		target := filepath.Join(dir, "elsewhere.sql")
		out := filepath.Join(dir, "hub.sql")
		require.NoError(t, os.Symlink(target, out))
		_, _, err := runBackupCLI(t, dsn, out)
		require.Error(t, err)
		require.Contains(t, err.Error(), "already exists")
		require.False(t, fake.ran(), "pg_dump must not run")
		require.NoFileExists(t, target)
	})

	t.Run("a failed dump leaves no file", func(t *testing.T) {
		initTestApp(t)
		fake := installFakePgDump(t)
		t.Setenv("MTIX_TEST_PGDUMP_FAIL", "1")
		out := filepath.Join(t.TempDir(), "hub.sql")
		_, _, err := runBackupCLI(t, dsn, out)
		require.Error(t, err)
		require.Contains(t, err.Error(), "pg_dump failed")
		require.True(t, fake.ran())
		require.NoFileExists(t, out, "a partial dump is removed")
	})
}

// TestBackup_TableArgs_EqualMigrationTables: the --table list handed to
// pg_dump is exactly the tables the hub migrations create, and the
// report names them (MTIX-95.7).
func TestBackup_TableArgs_EqualMigrationTables(t *testing.T) {
	initTestApp(t)
	fake := installFakePgDump(t)
	out := filepath.Join(t.TempDir(), "hub.sql")
	stdout, stderr, err := runBackupCLI(t,
		"postgres://backup_user:"+backupTestPassword+"@db.example.invalid:5432/hub", out)
	require.NoError(t, err, stderr)

	want, err := migrations.Tables()
	require.NoError(t, err)
	var got []string
	for _, arg := range fake.argv(t) {
		if name, ok := strings.CutPrefix(arg, "--table="); ok {
			got = append(got, name)
		}
	}
	sort.Strings(got)
	require.Equal(t, want, got, "pg_dump dumps every hub table and nothing else")
	require.Contains(t, stdout, "(tables: "+strings.Join(want, ", ")+")")
	require.Contains(t, stderr, "system trust store (PGSSLROOTCERT=system)",
		"with no CA configured the backup says which trust it uses")
	require.Contains(t, stderr, "sslrootcert=<ca.pem> in the DSN or with MTIX_SYNC_SSLROOTCERT")
}

// TestBackup_Interrupted_RemovesThePartialFile: a backup interrupted while
// pg_dump runs, by a cancelled context or by SIGINT or SIGTERM, stops
// pg_dump, removes the partial dump and says so, so an interrupted backup
// leaves no file either (MTIX-95.7).
func TestBackup_Interrupted_RemovesThePartialFile(t *testing.T) {
	const dsn = "postgres://backup_user:" + backupTestPassword + "@db.example.invalid:5432/hub"
	signalSelf := func(sig syscall.Signal) func(*testing.T, context.CancelFunc) {
		return func(t *testing.T, _ context.CancelFunc) {
			require.NoError(t, syscall.Kill(os.Getpid(), sig))
		}
	}
	tests := []struct {
		name      string
		interrupt func(*testing.T, context.CancelFunc)
	}{
		{"cancelled context", func(_ *testing.T, cancel context.CancelFunc) { cancel() }},
		{"SIGINT", signalSelf(syscall.SIGINT)},
		{"SIGTERM", signalSelf(syscall.SIGTERM)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			initTestApp(t)
			fake := installFakePgDump(t)
			t.Setenv("MTIX_TEST_PGDUMP_SLEEP", "30")
			t.Setenv(transport.EnvDSN, dsn)
			out := filepath.Join(t.TempDir(), "hub.sql")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan error, 1)
			go func() {
				var stdout, stderr bytes.Buffer
				done <- runSyncBackup(ctx, &stdout, &stderr, nil, out, transport.Options{})
			}()
			require.Eventually(t, fake.ran, 10*time.Second, 10*time.Millisecond, "pg_dump started")
			require.FileExists(t, out, "the output file exists while pg_dump runs")
			tt.interrupt(t, cancel)

			select {
			case err := <-done:
				require.Error(t, err)
				require.Contains(t, err.Error(), "interrupted")
			case <-time.After(20 * time.Second):
				t.Fatal("the backup did not stop after the interrupt")
			}
			require.NoFileExists(t, out, "an interrupted backup leaves no file")
		})
	}
}
