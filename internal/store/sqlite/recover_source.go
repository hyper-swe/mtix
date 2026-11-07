// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"errors"
	"fmt"
	"os"
)

var errEmptyRecoverySource = errors.New("zero-byte or one-byte database")

// checkRecoverDatabase protects the source before each SQLite salvage open.
// SQLite's Unix VFS treats one byte as empty too, and may unlink its WAL.
func checkRecoverDatabase(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat database: %w", err)
	}
	if info.Size() <= 1 {
		return fmt.Errorf("%w %s: SQLite salvage read skipped; no WAL-only salvage attempted; %s", errEmptyRecoverySource, path, preserveDatabaseSidecars)
	}
	return nil
}

// checkRecoveredSourceSize runs after the last SQLite salvage read, before
// acknowledgment coverage is finalized, so a growing source cannot look clean.
func checkRecoveredSourceSize(path string, before os.FileInfo, res *RecoverResult) {
	after, err := os.Stat(path)
	if before == nil || err != nil {
		res.InboxIncomplete = true
		res.Notes = append(res.Notes, "database size could not be verified across salvage; completeness cannot be proven")
		return
	}
	if before.Size() != after.Size() {
		res.InboxIncomplete = true
		res.Notes = append(res.Notes, fmt.Sprintf("database size changed during salvage (%d to %d bytes); completeness cannot be proven", before.Size(), after.Size()))
	}
}
