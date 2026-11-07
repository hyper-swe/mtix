// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
)

const preserveDatabaseSidecars = "keep the database and its -wal and -shm together: the WAL may hold the only copy of committed records, and SQLite treats such a file as empty and deletes its WAL when it opens it. To restore into this project, move all three files together out of the project (or the entire data/ directory); a copy leaves the refused files in place, so restoring here stays refused, and a copy is enough only for restoring into a fresh project. Do not move only the database"

// maxLinkHops bounds the walk along a chain of database symlinks.
const maxLinkHops = 40

// resolveDatabasePath returns the file SQLite will really open for path. A
// dangling link is followed through Readlink to the path it would create.
func resolveDatabasePath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	for hop := 0; hop < maxLinkHops; hop++ {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return path
		}
		target, err := os.Readlink(path)
		if err != nil {
			return path
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = target
	}
	return path
}

// guardEmptyDatabaseWAL must precede every SQLite open (openSQLite calls it,
// and Store.New calls it before the integrity escape hatch). SQLite can
// discard the WAL of an empty or one-byte database. A missing main file beside
// a WAL, including a dangling link whose target or link has one, is equally
// unsafe; a fresh store has no WAL.
func guardEmptyDatabaseWAL(path string) error {
	resolved := resolveDatabasePath(path)
	for _, candidate := range []string{resolved, path} {
		if err := guardWALBeside(candidate, resolved); err != nil {
			return err
		}
	}
	return nil
}

// guardWALBeside refuses when a nonempty WAL sits beside candidate while the
// database SQLite will open (resolved) is empty, one byte or missing.
func guardWALBeside(candidate, resolved string) error {
	walPath := candidate + "-wal"
	wal, err := os.Stat(walPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("stat WAL before open %s: %w", walPath, err)
	}
	if wal.Size() == 0 {
		return nil
	}
	// Observe the WAL first, then the database: a concurrent first opener may
	// initialize the database before committing its first WAL transaction.
	info, err := os.Stat(resolved)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat database before open: %w", err)
	}
	if info != nil && info.Size() > 1 {
		return nil
	}
	return fmt.Errorf("refusing to open empty, one-byte or missing database %s beside nonempty WAL %s: %w; run 'mtix recover'; %s", resolved, walPath, model.ErrCorrupted, preserveDatabaseSidecars)
}
