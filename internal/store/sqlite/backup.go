// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
)

// BackupResult holds information about a completed backup per FR-6.3a.
type BackupResult struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Verified bool   `json:"verified"`
}

// Backup creates a verified backup of the database to destPath per FR-6.3a.
// Uses VACUUM INTO for an atomic, consistent copy. After creation, the backup
// is opened read-only and verified with PRAGMA quick_check. If verification
// fails, the backup file is deleted and an error is returned.
func (s *Store) Backup(ctx context.Context, destPath string) (*BackupResult, error) {
	if destPath == "" {
		return nil, fmt.Errorf("destination path is required")
	}

	// VACUUM INTO creates an atomic, consistent copy of the database.
	// This is the recommended approach for SQLite backup per FR-6.3a.
	// Uses parameterized query — destPath is a string literal here because
	// VACUUM INTO does not support parameterized paths.
	// We validate the path doesn't contain SQL injection characters.
	vacuumSQL := fmt.Sprintf("VACUUM INTO '%s'", escapeSQLitePath(destPath))
	if _, err := s.writeDB.ExecContext(ctx, vacuumSQL); err != nil {
		return nil, fmt.Errorf("vacuum into %s: %w", destPath, err)
	}

	// Verify the backup by opening read-only and running PRAGMA quick_check.
	verified, verifyErr := verifyDatabase(ctx, destPath)
	if verifyErr != nil || !verified {
		// Delete corrupt backup per FR-6.3a.
		if removeErr := os.Remove(destPath); removeErr != nil {
			s.logger.Error("failed to remove corrupt backup",
				"path", destPath, "error", removeErr)
		}
		if verifyErr != nil {
			return nil, fmt.Errorf("verify backup %s: %w", destPath, verifyErr)
		}
		return nil, fmt.Errorf("backup verification failed for %s", destPath)
	}

	// Get file size.
	info, err := os.Stat(destPath)
	if err != nil {
		return nil, fmt.Errorf("stat backup %s: %w", destPath, err)
	}

	return &BackupResult{
		Path:     destPath,
		Size:     info.Size(),
		Verified: true,
	}, nil
}

// verifyDatabase opens a database read-only and runs PRAGMA quick_check.
// Returns true if the database passes verification.
func verifyDatabase(ctx context.Context, path string) (bool, error) {
	db, err := sql.Open("sqlite", path+"?mode=ro")
	if err != nil {
		return false, fmt.Errorf("open for verify: %w", err)
	}
	defer func() { _ = db.Close() }()

	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return false, fmt.Errorf("quick_check: %w", err)
	}

	return result == "ok", nil
}

// escapeSQLitePath escapes single quotes in a path for use in VACUUM INTO.
// This is necessary because VACUUM INTO does not support parameterized paths.
func escapeSQLitePath(path string) string {
	result := make([]byte, 0, len(path))
	for i := 0; i < len(path); i++ {
		if path[i] == '\'' {
			result = append(result, '\'', '\'')
		} else {
			result = append(result, path[i])
		}
	}
	return string(result)
}
