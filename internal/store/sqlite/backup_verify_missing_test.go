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
// Regression guard for MTIX-65, now FIXED. The defect: verifyDatabase
// reported verified=true, err=nil for a path that did not exist, and created
// a 0-byte file there as a side effect.
//
// Mechanism (two independently-safe decisions that were unsafe composed):
//  1. validateDBFile treats a missing file as "a fresh database, not
//     corruption" and returns nil — correct when OPENING a store, wrong when
//     VERIFYING an artifact that is supposed to already exist.
//  2. the verify DSN was built as path+"?mode=ro" with no "file:" URI scheme,
//     so SQLite did not honor mode=ro, CREATED the empty file, and
//     PRAGMA quick_check on a 0-byte database legitimately answered "ok".
//
// Consequence: backup verification — the last line of defense behind the
// 2026-05-19 data-loss incident — could green-light a backup that did not
// exist, and manufacture the empty file that made it look present.
//
// The fix gives verify its own existence/non-empty precondition instead of
// reusing the fresh-DB-tolerant validateDBFile, and builds the DSN as
// "file:"+path+"?mode=ro" so SQLite refuses to create a missing subject.
//
// Note on the neighbouring test: TestVerifyDatabase_NonExistent_ReturnsError
// did NOT catch this. It passed even against the buggy code, because its path
// "/nonexistent/file.db" sits in a directory that does not exist either, so
// SQLite could not create the file and it errored for an unrelated reason. It
// read as coverage of the missing-file case while leaving the real case
// untested: a missing file in a WRITABLE directory, which is every real
// backup directory. That is the case this test holds, and why the two are
// kept side by side rather than merged.
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
