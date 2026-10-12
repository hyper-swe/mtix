// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// The actual sync doctor producer must include local filesystem diagnostics.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

func TestSyncDoctor_FilesystemRow_PublicProducer(t *testing.T) {
	dir := t.TempDir()
	store := newDaemonTestStore(t, dir)
	t.Setenv(transport.EnvDSN, "")
	t.Setenv("MTIX_PG_TEST_DSN", "")
	saved := app
	app = appContext{mtixDir: dir, store: store, logger: slog.Default(), jsonOutput: true}
	t.Cleanup(func() { app = saved })
	var stdout, stderr bytes.Buffer
	require.ErrorIs(t, runSyncDoctor(context.Background(), &stdout, &stderr, nil, transport.Options{}), errDoctorChecksFailed, "existing missing hub still fails overall")
	var report DoctorReport
	require.NoError(t, json.Unmarshal(stdout.Bytes(), &report))
	found := false
	for _, check := range report.Checks {
		if check.Name == "data filesystem" {
			found = true
		}
	}
	require.True(t, found, "actual sync doctor must report the local data filesystem")
}

func TestSyncDoctor_FilesystemFixtures_TextAndJSON(t *testing.T) {
	cases := []struct {
		name, filesystem string
		unsafe           bool
		failure          error
		warn             bool
	}{
		{"local", "apfs", false, nil, false}, {"network", "nfs", true, nil, true}, {"fuse", "fuse", true, nil, true},
		{"unavailable", "", false, nil, true}, {"error", "", false, errors.New("classification failed"), true},
		{"known-local-ext", "ext", false, nil, false}, {"known-local-xfs", "xfs", false, nil, false},
		{"unrecognized-name", "someunknownfs", false, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := newDaemonTestStore(t, dir)
			t.Setenv(transport.EnvDSN, "")
			saved := app
			app = appContext{mtixDir: dir, store: store, logger: slog.Default()}
			t.Cleanup(func() { app = saved })
			inspect := func(path string) (string, bool, error) {
				require.Equal(t, filepath.Join(dir, "data"), path)
				return tc.filesystem, tc.unsafe, tc.failure
			}
			row := checkDataFilesystem([]func(string) (string, bool, error){inspect})
			require.True(t, row.Pass)
			require.Equal(t, tc.warn, row.Warn)
			require.True(t, appendDoctorCheck(DoctorReport{OverallPass: true}, row).OverallPass, "WARN never changes successful report to FAIL")
			for _, jsonOutput := range []bool{false, true} {
				app.jsonOutput = jsonOutput
				var stdout, stderr bytes.Buffer
				require.ErrorIs(t, runSyncDoctor(context.Background(), &stdout, &stderr, nil, transport.Options{}, inspect), errDoctorChecksFailed, "missing hub is existing overall failure")
				assertFilesystemDoctorOutput(t, stdout.Bytes(), row, jsonOutput)

			}
		})
	}
}

func assertFilesystemDoctorOutput(t *testing.T, output []byte, row DoctorCheck, jsonOutput bool) {
	t.Helper()
	if jsonOutput {
		var report DoctorReport
		require.NoError(t, json.Unmarshal(output, &report))
		require.False(t, report.OverallPass)
		matches := 0
		for _, check := range report.Checks {
			if check.Name == "data filesystem" {
				matches++
				require.Equal(t, row, check)
			}
		}
		require.Equal(t, 1, matches)
	} else {
		mark := "PASS"
		if row.Warn {
			mark = "WARN"
		}
		require.Contains(t, string(output), "["+mark+"] data filesystem")
		require.Contains(t, string(output), "FAILED")
	}
}
