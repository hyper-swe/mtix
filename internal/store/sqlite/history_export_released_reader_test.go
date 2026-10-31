// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite_test

// The released-reader proof for history.export (MTIX-95.31.19.6, ADR-006
// A3.1): the real v0.5.4-beta binary (compat054Binary builds or loads the tag
// commit and fails the test when it cannot; it never skips) imports the file
// written with the switch off, and refuses the one written with it on.

import (
	"crypto/sha256"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/testutil/hostguard"
)

// runReader054 runs the released reader in project and returns its combined
// output and exit code; the caller decides whether a failure is expected.
func runReader054(t *testing.T, binary, project string, args ...string) (string, int) {
	t.Helper()
	raw, err := os.ReadFile(binary)
	require.NoError(t, err)
	t.Logf("released reader commit=%s sha256=%x args=%v", compat054TagCommit, sha256.Sum256(raw), args)
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = project
	cmd.Env = append(hostguard.Environ(), "MTIX_SYNC_DSN=", "MTIX_SYNC_HOOK=")
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		exitErr, ok := err.(*exec.ExitError)
		require.True(t, ok, "released reader did not run: %v", err)
		code = exitErr.ExitCode()
	}
	t.Logf("released reader exit=%d output=%s", code, out)
	return string(out), code
}

func readerProject054(t *testing.T) string {
	t.Helper()
	project := t.TempDir()
	dir := filepath.Join(project, ".mtix")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "data"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("prefix: PROJ\nsync:\n  auto_sync: false\n"), 0o600))
	return project
}

func readerNodeCount054(t *testing.T, project string) int {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(project, ".mtix", "data", "mtix.db"))
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	var n int
	require.NoError(t, db.QueryRowContext(t.Context(), "SELECT count(*) FROM nodes").Scan(&n))
	return n
}

func TestHistoryExport_ReleasedReader054_SwitchOffImports_SwitchOnRefuses(t *testing.T) {
	reader := compat054Binary(t)
	f, raw := goldenStore(t)
	insertHistory(t, f.db, "01H0000000000000000000A001", goldenRootUID(t, raw), "status")
	ctx := t.Context()

	f.s.SetHistoryExportSource(func() bool { return false })
	off, err := f.s.Export(ctx, "PROJ", "0.0.0-golden")
	require.NoError(t, err)
	require.Equal(t, "2.0.0", off.SchemaVersion)
	offProject := readerProject054(t)
	offFile := filepath.Join(offProject, "incoming.json")
	require.NoError(t, os.WriteFile(offFile, marshalExport(t, off), 0o600))
	out, code := runReader054(t, reader, offProject, "import", offFile)
	require.Zero(t, code, "switch off: the released reader imports the file: %s", out)
	require.Equal(t, len(off.Nodes), readerNodeCount054(t, offProject))

	f.s.SetHistoryExportSource(func() bool { return true })
	f.s.SetWriterVersion(service.FirstHistoryVersion)
	on, err := f.s.Export(ctx, "PROJ", "")
	require.NoError(t, err)
	require.Equal(t, "2.1.0", on.SchemaVersion)
	onProject := readerProject054(t)
	onFile := filepath.Join(onProject, "incoming.json")
	require.NoError(t, os.WriteFile(onFile, marshalExport(t, on), 0o600))
	out, code = runReader054(t, reader, onProject, "import", onFile)
	require.NotZero(t, code, "switch on: the released reader refuses the file: %s", out)
	require.Contains(t, out, "checksum verification failed", "the exact refusal the docs record")
	require.Zero(t, readerNodeCount054(t, onProject), "a refused import stores nothing")
}

// The released-reader proof must never pass by skipping: a missing tag, cache
// or toolchain fails the test (compat054Binary requires success). This guard
// keeps a skip from being added to any file that obtains or uses the reader,
// this file included. The banned calls are spelled in pieces so that this
// file's own list is not a match.
func TestHistoryExport_ReleasedReaderTests_NeverSkip(t *testing.T) {
	files, err := filepath.Glob("*054*_test.go")
	require.NoError(t, err)
	files = append(files, "history_export_released_reader_test.go")
	banned := []string{"t.Sk" + "ip(", "t.Sk" + "ipf(", "t.Sk" + "ipNow(", "testing.Sh" + "ort()"}
	for _, f := range files {
		raw, err := os.ReadFile(f) //nolint:gosec // test sources in the package directory
		require.NoError(t, err)
		for _, b := range banned {
			require.NotContains(t, string(raw), b, "%s must not skip the released-reader proof", f)
		}
	}
	require.Greater(t, len(files), 5, "the glob must find the compat054 files")
}
