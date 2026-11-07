// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/require"
)

// linkedBoard turns the WAL-only board of walOnlyBoard into one whose
// .mtix/data/mtix.db is reached through the named link shape. With healthy
// the real database is the board's real database; without it the link
// dangles and the WAL (carrying the only copy of the rows) sits beside the
// path the link leads to. It returns the project root and the database file
// the links lead to (the link itself for a cycle).
func linkedBoard(t *testing.T, shape string, healthy bool) (string, string) {
	t.Helper()
	root, db := walOnlyBoard(t, 0)
	data := filepath.Dir(db)
	original, err := os.ReadFile(filepath.Join(app.mtixDir, "data", "mtix.db"))
	require.NoError(t, err)
	link := func(target, name string) { require.NoError(t, os.Symlink(target, name)) }
	move := func(realDB string) {
		for _, suffix := range []string{"-wal", "-shm"} {
			require.NoError(t, os.Rename(db+suffix, realDB+suffix))
		}
		require.NoError(t, os.Remove(db))
		if healthy {
			require.NoError(t, os.WriteFile(realDB, original, 0o600))
		}
	}
	far := filepath.Join(t.TempDir(), "far", "a", "b")
	farData := filepath.Join(far, "data")
	toFarData := func() {
		require.NoError(t, os.MkdirAll(far, 0o700))
		require.NoError(t, os.Rename(data, farData))
		link(farData, data)
	}
	switch shape {
	case "relative":
		realDB := filepath.Join(data, "real.db")
		move(realDB)
		link("real.db", db)
		return root, realDB
	case "absolute":
		realDB := filepath.Join(t.TempDir(), "real.db")
		move(realDB)
		link(realDB, db)
		return root, realDB
	case "dir-link-dotdot", "dir-link-two-hop":
		toFarData()
		realDB := filepath.Join(far, "real.db")
		db = filepath.Join(farData, "mtix.db")
		move(realDB)
		if shape == "dir-link-dotdot" {
			link(filepath.Join("..", "real.db"), db)
		} else {
			link("mid.db", db)
			link(filepath.Join("..", "real.db"), filepath.Join(farData, "mid.db"))
		}
		return root, realDB
	case "chain":
		realDB := filepath.Join(data, "real.db")
		move(realDB)
		for i := 1; i <= 44; i++ {
			next := fmt.Sprintf("l%d.db", i+1)
			if i == 44 {
				next = "real.db"
			}
			link(next, filepath.Join(data, fmt.Sprintf("l%d.db", i)))
		}
		link("l1.db", db)
		return root, realDB
	case "cycle":
		require.NoError(t, os.Remove(db))
		link("other.db", db)
		link("mtix.db", filepath.Join(data, "other.db"))
		return root, db
	}
	t.Fatalf("unknown link shape %q", shape)
	return "", ""
}

// NFR-2.8: through the real CLI, a dangling database link is followed the way
// SQLite follows it (.. after a linked data directory, chains, 45 hops, with
// or without the integrity bypass): the command is refused, the WAL and shm
// beside the real target are untouched, and the target is not created.
func TestCLI_DanglingLinkShapes_RefuseAndPreserveSidecars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	for _, shape := range []string{"relative", "absolute", "dir-link-dotdot", "dir-link-two-hop", "chain", "cycle"} {
		for _, skip := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/skip-integrity=%t", shape, skip), func(t *testing.T) {
				if skip {
					t.Setenv("MTIX_SKIP_INTEGRITY_CHECK", "1")
				}
				root, realDB := linkedBoard(t, shape, false)
				before := hashFiles(t, realDB+"-wal", realDB+"-shm")
				out := doctorCLIExecute(t, root, "", "list")
				require.NotEqual(t, 0, out.exit, "stderr: %s", out.stderr)
				if shape != "cycle" {
					require.Equal(t, 4, out.exit, "stderr: %s", out.stderr)
					_, err := os.Stat(realDB)
					require.True(t, os.IsNotExist(err), "must not create the link target")
				}
				require.Equal(t, before, hashFiles(t, realDB+"-wal", realDB+"-shm"))
			})
		}
	}
}

// A healthy store reached through absolute, relative, chained and data
// directory links is never refused by the stricter walk.
func TestCLI_HealthyStoreThroughLinks_IsNotRefused(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	for _, shape := range []string{"relative", "absolute", "dir-link-dotdot", "dir-link-two-hop"} {
		t.Run(shape, func(t *testing.T) {
			root, _ := linkedBoard(t, shape, true)
			for _, command := range []string{"list", "ready"} {
				out := doctorCLIExecute(t, root, "", command)
				require.Equal(t, 0, out.exit, "%s stderr: %s", command, out.stderr)
			}
		})
	}
}

// NFR-2.8: mtix backup names its destination, and SQLite would reset an empty
// or one-byte destination and delete the WAL beside it. The backup is refused
// and the destination's WAL and shm are untouched.
func TestCLI_BackupShortDestinationBesideWAL_RefusesAndPreservesSidecars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	for _, where := range []string{"empty", "one-byte", "dangling-link"} {
		t.Run(where, func(t *testing.T) {
			root, db := walOnlyBoard(t, 0)
			original, err := os.ReadFile(filepath.Join(app.mtixDir, "data", "mtix.db"))
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(db, original, 0o600))
			dir := t.TempDir()
			dest, holder := filepath.Join(dir, "backup.db"), filepath.Join(dir, "backup.db")
			switch where {
			case "empty":
				require.NoError(t, os.WriteFile(dest, nil, 0o600))
			case "one-byte":
				require.NoError(t, os.WriteFile(dest, []byte{0}, 0o600))
			default:
				holder = filepath.Join(dir, "real.db")
				require.NoError(t, os.Symlink(holder, dest))
			}
			wal, err := os.ReadFile(db + "-wal")
			require.NoError(t, err)
			require.NoError(t, os.WriteFile(holder+"-wal", wal, 0o600))
			require.NoError(t, os.WriteFile(holder+"-shm", []byte("shm"), 0o600))
			before := hashFiles(t, holder+"-wal", holder+"-shm")
			out := doctorCLIExecute(t, root, "", "backup", dest)
			require.NotEqual(t, 0, out.exit, "stdout: %s stderr: %s", out.stdout, out.stderr)
			require.Equal(t, before, hashFiles(t, holder+"-wal", holder+"-shm"))
			_, statErr := os.Stat(filepath.Join(dir, "real.db"))
			require.True(t, os.IsNotExist(statErr), "must not create the link target")
		})
	}
}
