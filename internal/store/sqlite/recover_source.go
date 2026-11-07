// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"fmt"
	"os"
)

// checkRecoverDatabase protects the damaged source before each SQLite salvage
// open. SQLite can remove a WAL beside a zero-byte database even in mode=ro.
func checkRecoverDatabase(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat database: %w", err)
	}
	if info.Size() == 0 {
		return fmt.Errorf("zero-byte database %s skipped to preserve the database and WAL; no database or WAL-only salvage attempted; keep both files together", path)
	}
	return nil
}
