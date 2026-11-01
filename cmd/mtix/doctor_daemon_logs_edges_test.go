// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
)

func TestDoctorDaemonLogs_NonregularReadOnly(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			target := createDaemonLogFixture(t, root, "retained.log", 42)
			before := snapshotDaemonLog(t, target)
			path := filepath.Join(root, ".mtix", "logs", "daemon.out.log")
			want := "not a regular file"
			if kind == "symlink" {
				if runtime.GOOS == "windows" {
					t.Skip("creating Windows symlinks requires optional privileges")
				}
				require.NoError(t, os.Symlink(target, path))
				want = "symlink, not inspected"
			} else {
				require.NoError(t, os.Mkdir(path, 0700))
			}
			info, err := os.Lstat(path)
			require.NoError(t, err)
			rows := runDaemonLogRegistry(t, filepath.Join(root, ".mtix"))
			require.Len(t, rows, 1)
			require.Equal(t, service.DoctorWarn, rows[0].Status)
			require.Contains(t, rows[0].Detail, "daemon.out.log")
			require.Contains(t, rows[0].Detail, want)
			require.NotContains(t, rows[0].Detail, "warning above")
			require.Contains(t, rows[0].Fix, "Inspect .mtix/logs")
			after, err := os.Lstat(path)
			require.NoError(t, err)
			require.True(t, os.SameFile(info, after))
			require.Equal(t, info.Mode(), after.Mode())
			assertDaemonLogSnapshot(t, before, snapshotDaemonLog(t, target))
		})
	}
}

func TestDoctorDaemonLogs_InspectionErrors(t *testing.T) {
	for _, kind := range []string{"not-directory", "unreadable-directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			logs := filepath.Join(dir, "logs")
			if kind == "not-directory" {
				require.NoError(t, os.WriteFile(logs, []byte("retain"), 0600))
			} else {
				if os.Geteuid() == 0 || runtime.GOOS == "windows" {
					t.Skip("permission denial requires a non-root Unix process")
				}
				require.NoError(t, os.Mkdir(logs, 0700))
				t.Cleanup(func() { require.NoError(t, os.Chmod(logs, 0700)) })
				require.NoError(t, os.Chmod(logs, 0))
			}
			before, err := os.Lstat(logs)
			require.NoError(t, err)
			rows := runDaemonLogRegistry(t, dir)
			require.Len(t, rows, 1)
			require.Equal(t, service.DoctorWarn, rows[0].Status)
			for _, name := range []string{"daemon.out.log", "daemon.err.log"} {
				require.Contains(t, rows[0].Detail, "cannot inspect .mtix/logs/"+name)
			}
			require.Contains(t, rows[0].Fix, "Inspect .mtix/logs")
			require.Equal(t, 1, strings.Count(rows[0].Fix, "Inspect .mtix/logs"))
			after, err := os.Lstat(logs)
			require.NoError(t, err)
			require.True(t, os.SameFile(before, after))
			require.Equal(t, before.Mode(), after.Mode())
			require.Equal(t, before.ModTime(), after.ModTime())
		})
	}
}

func TestDoctorDaemonLogs_EmptyBoardIgnoresRelativeLogs(t *testing.T) {
	root := t.TempDir()
	path := createDaemonLogFixture(t, root, "daemon.err.log", 10*1024*1024+1)
	before := snapshotDaemonLog(t, path)
	t.Chdir(filepath.Join(root, ".mtix"))
	require.Empty(t, runDaemonLogRegistry(t, ""))
	assertDaemonLogSnapshot(t, before, snapshotDaemonLog(t, path))
}

func TestDoctorDaemonLogsCLI_BothOversizedOneRemedy(t *testing.T) {
	root := doctorCLIBoard(t, "")
	outPath := createDaemonLogFixture(t, root, "daemon.out.log", 10*1024*1024+1)
	errPath := createDaemonLogFixture(t, root, "daemon.err.log", 10*1024*1024+2)
	outBefore, errBefore := snapshotDaemonLog(t, outPath), snapshotDaemonLog(t, errPath)
	out := doctorCLIExecute(t, root, "", "doctor", "--json", "--fix")
	require.Equal(t, 0, out.exit, out.stderr)
	row := daemonLogDoctorRow(t, out.stdout)
	require.NotNil(t, row)
	require.Equal(t, "warn", row["status"])
	require.Contains(t, row["detail"], "daemon.out.log: 10485761")
	require.Contains(t, row["detail"], "daemon.err.log: 10485762")
	require.Equal(t, 1, strings.Count(row["fix"], "sudo /usr/sbin/newsyslog"))
	require.Equal(t, 1, strings.Count(row["fix"], "sudo logrotate"))
	assertDaemonLogSnapshot(t, outBefore, snapshotDaemonLog(t, outPath))
	assertDaemonLogSnapshot(t, errBefore, snapshotDaemonLog(t, errPath))
}

func runDaemonLogRegistry(t *testing.T, dir string) []service.DoctorResult {
	t.Helper()
	saveAndResetApp(t)
	app.mtixDir = dir
	svc := service.NewDoctorService(service.DoctorOptions{})
	require.NoError(t, registerDaemonLogsCheck(svc))
	rows, err := svc.Run(t.Context(), service.DoctorRunOptions{Fix: true})
	require.NoError(t, err)
	return rows
}

func assertDaemonLogSnapshot(t *testing.T, before, after daemonLogSnapshot) {
	t.Helper()
	require.Equal(t, before.hash, after.hash)
	require.Equal(t, before.info.ModTime(), after.info.ModTime())
	require.True(t, os.SameFile(before.info, after.info))
}
