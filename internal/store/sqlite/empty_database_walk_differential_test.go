// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"database/sql"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Differential seed and size. The seed is fixed so the bar is the same on
// every run; the counts keep the test to a few seconds.
const (
	walkFuzzSeed     = 20261108
	walkFuzzLayouts  = 120
	walkFuzzOpens    = 50
	walkFuzzMinAgree = 1000
)

var walkFuzzNames = []string{"a", "b", "c", "d", "..", "..", ".", "m", "n", "f", "l1", "l2", "l3"}

// walkFuzzTarget makes a random link target of one to five components,
// sometimes with a trailing slash and sometimes absolute under root.
func walkFuzzTarget(r *rand.Rand, root string) string {
	parts := make([]string, 1+r.Intn(5))
	for i := range parts {
		parts[i] = walkFuzzNames[r.Intn(len(walkFuzzNames))]
	}
	target := strings.Join(parts, "/")
	if r.Intn(5) == 0 {
		target += "/"
	}
	if r.Intn(5) == 0 {
		target = root + "/" + target
	}
	return target
}

// walkFuzzLayout builds a small tree under root (directories a/b, a/c, d, a
// file f) and links l1, l2, l3, a/l1, a/b/l2 and d/l3 with random targets, so
// links point at directories, files, missing names, ".." and each other.
func walkFuzzLayout(t *testing.T, r *rand.Rand, root string) string {
	t.Helper()
	for _, dir := range []string{"a/b", "a/c", "d"} {
		require.NoError(t, os.MkdirAll(filepath.Join(root, dir), 0o700))
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "f"), []byte("x"), 0o600))
	var layout []string
	for _, link := range []string{"l1", "l2", "l3", "a/l1", "a/b/l2", "d/l3"} {
		if r.Intn(2) == 0 {
			target := walkFuzzTarget(r, root)
			require.NoError(t, os.Symlink(target, filepath.Join(root, link)))
			layout = append(layout, link+"->"+strings.ReplaceAll(target, root, "$R"))
		}
	}
	return strings.Join(layout, " ")
}

// walkFuzzOpenPath makes a random path to open under root, one to five
// components drawn from the same names, so it can run through links, missing
// names and "..".
func walkFuzzOpenPath(r *rand.Rand, root string) string {
	parts := make([]string, 1+r.Intn(5))
	for i := range parts {
		parts[i] = walkFuzzNames[r.Intn(len(walkFuzzNames))]
	}
	return root + "/" + strings.Join(parts, "/")
}

// sqliteResolvedFile asks real SQLite which file it opens for path. An error
// means SQLite could not open the path at all.
func sqliteResolvedFile(path string) (string, error) {
	db, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return "", err
	}
	defer func() { _ = db.Close() }()
	var seq int
	var name, file string
	if err := db.QueryRow("PRAGMA database_list").Scan(&seq, &name, &file); err != nil {
		return "", err
	}
	return file, nil
}

// NFR-2.8: the guard must name the same file real SQLite opens. Over random
// link layouts and open paths (fixed seed, bounded count) it fails on any
// path where SQLite opens a file and the guard names another, or refuses.
// It is a differential test: SQLite is the oracle, the guard is the subject.
func TestResolveDatabasePath_RandomLinkLayouts_MatchSQLiteOpenedFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks require Windows privileges")
	}
	r := rand.New(rand.NewSource(walkFuzzSeed)) //nolint:gosec // G404: a fixed seed for a reproducible test layout, not a secret.
	// SQLite creates a file at the path it opens, and ".." in a random path
	// can climb out of a layout, so the layouts sit three levels inside the
	// test directory and are named apart from the generated names.
	base, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	base = filepath.Join(base, "s1", "s2", "s3")
	require.NoError(t, os.MkdirAll(base, 0o700))
	agree, disagree := 0, []string{}
	for i := 0; i < walkFuzzLayouts; i++ {
		root := filepath.Join(base, fmt.Sprintf("L%d", i))
		layout := walkFuzzLayout(t, r, root)
		for j := 0; j < walkFuzzOpens; j++ {
			open := walkFuzzOpenPath(r, root)
			got, guardErr := resolveDatabasePath(open)
			want, sqliteErr := sqliteResolvedFile(open)
			switch {
			case sqliteErr != nil:
				// SQLite cannot open it, so no WAL can be lost through it.
			case guardErr != nil:
				disagree = append(disagree, fmt.Sprintf("guard refuses what SQLite opens: open=%s layout=[%s] sqlite=%s err=%v",
					strings.TrimPrefix(open, root), layout, strings.ReplaceAll(want, root, "$R"), guardErr))
			case got != want:
				disagree = append(disagree, fmt.Sprintf("guard and SQLite reach different files: open=%s layout=[%s]\n  guard =%s\n  sqlite=%s",
					strings.TrimPrefix(open, root), layout, strings.ReplaceAll(got, root, "$R"), strings.ReplaceAll(want, root, "$R")))
			default:
				agree++
			}
		}
	}
	require.Empty(t, disagree, "%d disagreements, first: %s", len(disagree), firstOf(disagree))
	require.GreaterOrEqual(t, agree, walkFuzzMinAgree, "too few openable paths: the generator no longer reaches SQLite")
}

func firstOf(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[0]
}
