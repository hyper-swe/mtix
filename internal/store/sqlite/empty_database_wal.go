// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
)

const preserveDatabaseSidecars = "keep the database and its -wal and -shm together: the WAL may hold the only copy of committed records. Before restoring, copy or move all three files together out of the project (or the entire data/ directory); do not move only the database"

// guardEmptyDatabaseWAL must precede every Store open, including the integrity
// escape hatch. SQLite can discard the WAL of an empty or one-byte database.
// A missing main file beside a WAL is equally unsafe; a fresh store has no WAL.
func guardEmptyDatabaseWAL(path string) error {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		path = resolved
	}
	walPath := path + "-wal"
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
	info, err := os.Stat(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("stat database before open: %w", err)
	}
	if info != nil && info.Size() > 1 {
		return nil
	}
	return fmt.Errorf("refusing to open empty, one-byte or missing database %s beside nonempty WAL %s: %w; run 'mtix recover'; %s", path, walPath, model.ErrCorrupted, preserveDatabaseSidecars)
}
