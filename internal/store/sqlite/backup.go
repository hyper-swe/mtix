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

	// Pre-flight per NFR-2.8: VACUUM INTO writes a full copy of the
	// database, so the destination volume must hold the current DB plus
	// WAL plus the configured floor. Refusing up front beats failing
	// halfway on a full disk.
	if err := s.preflightBackup(destPath); err != nil {
		return nil, err
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

// verifyDatabase opens a database read-only and runs the shared
// quick_check. The NFR-2.6a truncation validation runs first so a torn
// backup is reported diagnostically ("truncated") instead of as an opaque
// quick_check failure.
//
// The verification subject must already EXIST and be non-empty (MTIX-65):
// verify is asked about an artifact — a backup — not a store that may
// legitimately be fresh, so validateDBFile's missing-file-is-a-fresh-DB
// leniency does not apply here. The old path composed two individually
// correct decisions into a false-pass: validateDBFile returned nil for a
// missing file, and the DSN `path+"?mode=ro"` carried no `file:` URI scheme,
// so mode=ro was not honored — SQLite CREATED a 0-byte file and quick_check
// on an empty database legitimately answered "ok". A nonexistent backup
// verified as good and the verifier manufactured the artifact that made it
// look present. A verification that cannot run must never read as a
// verification that passed.
func verifyDatabase(ctx context.Context, path string) (bool, error) {
	info, statErr := os.Stat(path)
	if statErr != nil {
		return false, fmt.Errorf("verify %s: backup does not exist: %w", path, statErr)
	}
	if info.Size() == 0 {
		return false, fmt.Errorf("verify %s: backup is 0 bytes — not a database", path)
	}
	if err := validateDBFile(path); err != nil {
		return false, err
	}

	// file: URI scheme so mode=ro is honored — verification must never
	// write, let alone create, its subject.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return false, fmt.Errorf("open for verify: %w", err)
	}
	defer func() { _ = db.Close() }()

	result, err := quickCheck(ctx, db)
	if err != nil {
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
