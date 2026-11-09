// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"crypto/sha256"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// walOnlyBoard builds a project whose data directory holds an empty database
// beside a WAL that carries the only copy of committed rows, then returns the
// project root and the database path. Commands reach it through initApp with
// the data DIRECTORY, the path every CLI command uses (NFR-2.8).
func walOnlyBoard(t *testing.T, size int) (string, string) {
	t.Helper()
	initTestApp(t)
	for _, q := range []string{`PRAGMA wal_autocheckpoint=0`, `PRAGMA wal_checkpoint(TRUNCATE)`} {
		_, err := app.store.WriteDB().Exec(q)
		require.NoError(t, err)
	}
	_, err := app.store.WriteDB().Exec(`INSERT INTO sync_events(event_id,project_prefix,node_id,op_type,payload,wall_clock_ts,lamport_clock,vector_clock,author_id,author_machine_hash,created_at)
    VALUES('wal-cli-only','TEST','TEST-1','comment','{"to":"worker","body":"WAL only mail"}',0,1,'{}','sender','','2026-11-05T00:00:00Z')`)
	require.NoError(t, err)
	root := t.TempDir()
	data := filepath.Join(root, ".mtix", "data")
	require.NoError(t, os.MkdirAll(data, 0o700))
	config, err := os.ReadFile(filepath.Join(app.mtixDir, "config.yaml"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, ".mtix", "config.yaml"), config, 0o600))
	db := filepath.Join(data, "mtix.db")
	require.NoError(t, os.WriteFile(db, make([]byte, size), 0o600))
	for _, suffix := range []string{"-wal", "-shm"} {
		raw, err := os.ReadFile(filepath.Join(app.mtixDir, "data", "mtix.db"+suffix))
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(db+suffix, raw, 0o600))
	}
	return root, db
}

func hashFiles(t *testing.T, paths ...string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	for _, p := range paths {
		raw, err := os.ReadFile(p)
		require.NoError(t, err)
		out[p] = sha256.Sum256(raw)
	}
	return out
}

// NFR-2.8: through the real CLI and the data directory, an empty database
// beside a WAL is refused (exit 4) and all three files are untouched.
func TestCLI_EmptyDatabaseBesideWAL_RefusesExit4AndPreservesFiles(t *testing.T) {
	for _, size := range []int{0, 1} {
		for _, args := range [][]string{{"list"}, {"ready"}} {
			name := args[0] + map[int]string{0: "-0byte", 1: "-1byte"}[size]
			t.Run(name, func(t *testing.T) {
				root, db := walOnlyBoard(t, size)
				before := hashFiles(t, db, db+"-wal", db+"-shm")
				out := doctorCLIExecute(t, root, "", args...)
				require.Equal(t, 4, out.exit, "stderr: %s", out.stderr)
				require.Contains(t, out.stderr, "-wal")
				require.Equal(t, before, hashFiles(t, db, db+"-wal", db+"-shm"))
			})
		}
	}
}

// NFR-2.8: a dangling database link whose target has a WAL is refused too;
// SQLite would otherwise create the target and unlink the WAL.
func TestCLI_DanglingDatabaseSymlinkBesideWAL_RefusesAndPreservesWAL(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	root, db := walOnlyBoard(t, 0)
	target := filepath.Join(t.TempDir(), "real.db")
	require.NoError(t, os.Remove(db))
	require.NoError(t, os.Remove(db+"-shm"))
	require.NoError(t, os.Rename(db+"-wal", target+"-wal"))
	require.NoError(t, os.Symlink(target, db))
	before := hashFiles(t, target+"-wal")
	out := doctorCLIExecute(t, root, "", "list")
	require.NotEqual(t, 0, out.exit, "stderr: %s", out.stderr)
	require.Equal(t, before, hashFiles(t, target+"-wal"))
	_, err := os.Stat(target)
	require.True(t, os.IsNotExist(err), "must not create the link target")
}
