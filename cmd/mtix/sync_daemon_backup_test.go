// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
	"github.com/stretchr/testify/require"
)

// PG-free CLI tests for mtix sync daemon and backup.

// --- daemon ---

func TestSyncDaemonCmd_Construction(t *testing.T) {
	cmd := newSyncDaemonCmd()
	require.Equal(t, "daemon", cmd.Use)
	for _, name := range []string{"insecure-tls", "interval", "install"} {
		require.NotNilf(t, cmd.Flags().Lookup(name), "%s flag declared", name)
	}
}

func TestSyncDaemonCmd_InstallStub(t *testing.T) {
	var buf bytes.Buffer
	require.NoError(t, printDaemonInstallStub(&buf))
	out := buf.String()
	require.Contains(t, out, "[Unit]", "systemd unit section")
	require.Contains(t, out, "[Service]")
	require.Contains(t, out, "ExecStart=/usr/local/bin/mtix sync daemon")
	require.Contains(t, out, "MTIX_SYNC_DSN")
	require.Contains(t, out, "launchd", "darwin section noted")
}

func TestRunSyncDaemon_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncDaemon(context.Background(), &stdout, &stderr,
		[]string{"postgres://u:p@h/d"}, transport.Options{InsecureTLS: true}, 30, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

func TestDaemonPIDFile_StaleIsTreatedAsAbsent(t *testing.T) {
	dir := t.TempDir()
	// Write a PID that's almost certainly NOT a live process
	// (PID 1 IS live on Unix; pick something high and unlikely).
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, daemonPIDFilename),
		[]byte("999999999"),
		0o600,
	))
	live, _, err := daemonPIDFileLive(dir)
	require.NoError(t, err)
	require.False(t, live, "non-existent PID treated as stale")

	// A diagnostic probe must not delete a marker without ownership.
	_, statErr := os.Stat(filepath.Join(dir, daemonPIDFilename))
	require.NoError(t, statErr)
}

func TestDaemonPIDFile_GarbageContent(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(
		filepath.Join(dir, daemonPIDFilename),
		[]byte("not-a-pid"),
		0o600,
	))
	live, _, err := daemonPIDFileLive(dir)
	require.NoError(t, err)
	require.False(t, live, "garbage content treated as stale")
	body, err := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	require.NoError(t, err)
	require.Equal(t, "not-a-pid", string(body), "diagnostic probe cannot delete malformed marker")
}

func TestDaemonPIDFile_LiveSelf(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeDaemonPID(dir, os.Getpid()))
	live, pid, err := daemonPIDFileLive(dir)
	require.NoError(t, err)
	require.True(t, live, "current process IS live")
	require.Equal(t, os.Getpid(), pid)
}

func TestDaemonPIDFile_AbsentIsNotLive(t *testing.T) {
	dir := t.TempDir()
	live, _, err := daemonPIDFileLive(dir)
	require.NoError(t, err)
	require.False(t, live)
}

func TestWriteDaemonPID_ModeIs0600(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, writeDaemonPID(dir, 12345))
	info, err := os.Stat(filepath.Join(dir, daemonPIDFilename))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	body, err := os.ReadFile(filepath.Join(dir, daemonPIDFilename))
	require.NoError(t, err)
	pid, err := strconv.Atoi(string(body))
	require.NoError(t, err)
	require.Equal(t, 12345, pid)
}

func TestRemoveDaemonPID_AbsentIsNoop(t *testing.T) {
	var owner *daemonOwner
	require.NoError(t, owner.release())
	require.NoError(t, (&daemonOwner{}).release())
}

// --- backup ---

func TestSyncBackupCmd_Construction(t *testing.T) {
	cmd := newSyncBackupCmd()
	require.Equal(t, "backup", cmd.Use)
	require.NotNil(t, cmd.Flags().Lookup("output"))
}

func TestRunSyncBackup_RefusesEmptyOutput(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = t.TempDir()
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncBackup(context.Background(), &stdout, &stderr,
		[]string{"postgres://u:p@h/d"}, "", transport.Options{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "--output is required")
}

func TestRunSyncBackup_RefusesOutsideMtixProject(t *testing.T) {
	saved := app.mtixDir
	app.mtixDir = ""
	t.Cleanup(func() { app.mtixDir = saved })

	var stdout, stderr bytes.Buffer
	err := runSyncBackup(context.Background(), &stdout, &stderr,
		[]string{"postgres://u:p@h/d"}, "/tmp/backup.sql", transport.Options{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not in an mtix project")
}

// TestSyncBackupCmd_InsecureTLSFlag: backup takes --insecure-tls like the
// other sync commands, since its connection follows the same TLS rules
// (MTIX-95.7).
func TestSyncBackupCmd_InsecureTLSFlag(t *testing.T) {
	cmd := newSyncBackupCmd()
	f := cmd.Flags().Lookup("insecure-tls")
	require.NotNil(t, f)
	require.Equal(t, "false", f.DefValue)
}

func TestPgDumpBin_Default(t *testing.T) {
	t.Setenv("MTIX_PG_DUMP", "")
	require.Equal(t, "pg_dump", pgDumpBin())
}

func TestPgDumpBin_OverrideViaEnv(t *testing.T) {
	t.Setenv("MTIX_PG_DUMP", "/usr/local/bin/pg_dump-15")
	require.Equal(t, "/usr/local/bin/pg_dump-15", pgDumpBin())
}

// The final command-registration check moved to sync_backfill_test.go
// as TestSyncCmd_AllElevenFR18CommandsRegistered when MTIX-15.13.1
// added the 11th subcommand.
