// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Exercise the stock command so removing doctor registration breaks the test.
func TestDoctorDaemonLogsCLI_OversizedReadOnly(t *testing.T) {
	for _, name := range []string{"daemon.out.log", "daemon.err.log"} {
		t.Run(name, func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			path := createDaemonLogFixture(t, root, name, 10*1024*1024+1)
			before := snapshotDaemonLog(t, path)
			for _, args := range [][]string{{"doctor"}, {"doctor", "--json"}, {"doctor", "--fix"}, {"doctor", "--json", "--fix"}} {
				out := doctorCLIExecute(t, root, "", args...)
				require.Equal(t, 0, out.exit, out.stderr)
				require.Contains(t, out.stdout, "daemon.logs")
				require.Contains(t, out.stdout, "generated sync skill")
				require.Contains(t, out.stdout, name)
				require.Contains(t, out.stdout, "10485761")
				require.Contains(t, out.stdout, "10485760")
				require.Contains(t, out.stdout, "sudo /usr/sbin/newsyslog -f /etc/newsyslog.d/mtix-daemon.conf")
				require.Contains(t, out.stdout, "sudo logrotate /etc/logrotate.d/mtix-daemon")
				if strings.Contains(strings.Join(args, " "), "--json") {
					row := daemonLogDoctorRow(t, out.stdout)
					require.NotNil(t, row)
					require.Equal(t, "warn", row["status"])
					require.NotEmpty(t, row["fix"])
				} else {
					require.Contains(t, out.stdout, "[WARN] daemon.logs")
				}
				after := snapshotDaemonLog(t, path)
				require.Equal(t, before.hash, after.hash)
				require.Equal(t, before.info.ModTime(), after.info.ModTime())
				require.True(t, os.SameFile(before.info, after.info))
			}
		})
	}
}

func TestDoctorDaemonLogsCLI_ThresholdAndAbsent(t *testing.T) {
	for name, size := range map[string]int64{"below": 10*1024*1024 - 1, "at": 10 * 1024 * 1024} {
		t.Run(name, func(t *testing.T) {
			root := doctorCLIBoard(t, "")
			createDaemonLogFixture(t, root, "daemon.err.log", size)
			out := doctorCLIExecute(t, root, "", "doctor", "--json")
			require.Equal(t, 0, out.exit, out.stderr)
			row := daemonLogDoctorRow(t, out.stdout)
			require.NotNil(t, row)
			require.Equal(t, "pass", row["status"])
			require.Empty(t, row["fix"])
		})
	}
	root := doctorCLIBoard(t, "")
	out := doctorCLIExecute(t, root, "", "doctor", "--json", "--fix")
	require.Equal(t, 0, out.exit, out.stderr)
	require.Nil(t, daemonLogDoctorRow(t, out.stdout))
	_, err := os.Stat(filepath.Join(root, ".mtix", "logs"))
	require.True(t, os.IsNotExist(err), "doctor must not create the absent log directory")
}

func createDaemonLogFixture(t *testing.T, root, name string, size int64) string {
	t.Helper()
	dir := filepath.Join(root, ".mtix", "logs")
	require.NoError(t, os.MkdirAll(dir, 0700))
	path := filepath.Join(dir, name)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	require.NoError(t, err)
	_, writeErr := file.WriteString("preserve this diagnostic record\n")
	require.NoError(t, writeErr)
	require.NoError(t, file.Truncate(size))
	require.NoError(t, file.Close())
	marker := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	require.NoError(t, os.Chtimes(path, marker, marker))
	return path
}

type daemonLogSnapshot struct {
	info os.FileInfo
	hash [32]byte
}

func snapshotDaemonLog(t *testing.T, path string) daemonLogSnapshot {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err)
	info, err := os.Stat(path)
	require.NoError(t, err)
	return daemonLogSnapshot{info: info, hash: sha256.Sum256(raw)}
}

func daemonLogDoctorRow(t *testing.T, body string) map[string]string {
	t.Helper()
	for _, row := range doctorCLIRows(t, body) {
		if row["id"] == "daemon.logs" {
			return row
		}
	}
	return nil
}
