// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestVerifyDatabase_MissingFile_FailsVerification pins the rule that a
// verification which CANNOT RUN must never read as a verification that
// PASSED.
//
// Defect: verifyDatabase reports verified=true, err=nil for a path that does
// not exist, and creates a 0-byte file there as a side effect.
//
// Mechanism (two independently-safe decisions that are unsafe composed):
//  1. validateDBFile treats a missing file as "a fresh database, not
//     corruption" and returns nil — correct when OPENING a store, wrong when
//     VERIFYING an artifact that is supposed to already exist.
//  2. the verify DSN is built as path+"?mode=ro" with no "file:" URI scheme,
//     so SQLite does not honor mode=ro, CREATES the empty file, and
//     PRAGMA quick_check on a 0-byte database legitimately answers "ok".
//
// Consequence: backup verification — the last line of defense behind the
// 2026-05-19 data-loss incident — can green-light a backup that does not
// exist, and manufacture the empty file that makes it look present.
//
// Note on existing coverage: TestVerifyDatabase_NonExistent_ReturnsError
// passes only because its path "/nonexistent/file.db" sits in a directory
// that does not exist either, so SQLite cannot create the file and errors for
// an unrelated reason. It reads as coverage of the missing-file case while
// leaving the real case — a missing file in a writable directory, which is
// every real backup directory — untested.
//
// This test is EXPECTED TO FAIL until the defect is fixed. Filed, not fixed.
func TestVerifyDatabase_MissingFile_FailsVerification(t *testing.T) {
	// A writable directory that really exists — every real backup dir.
	dir := t.TempDir()
	missing := filepath.Join(dir, "nightly-backup.db")

	_, statErr := os.Stat(missing)
	require.True(t, os.IsNotExist(statErr), "precondition: the backup must not exist")

	verified, err := verifyDatabase(context.Background(), missing)

	assert.Error(t, err, "verifying a non-existent backup must fail loudly")
	assert.False(t, verified, "a backup that does not exist must never verify as OK")

	_, statErr = os.Stat(missing)
	assert.True(t, os.IsNotExist(statErr),
		"verification must not CREATE the artifact it was asked to verify")
}
