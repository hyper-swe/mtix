// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// White-box tests for NFR-2.8 (disk-full safety): explicit durability
// pragmas, truncation detection before open, quick_check on open,
// free-space pre-flight, and fail-stop latching. These are the unit-level
// guards behind the fault-injection e2e suite in e2e/faultinject.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/hyper-swe/mtix/internal/model"
)

// newDurabilityTestStore creates a store in a temp dir and returns it with
// its database path. White-box twin of store_test.go's newTestStore.
func newDurabilityTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	s, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s, dbPath
}

// seedRows writes enough data that the database spans multiple pages, so
// mid-file corruption in tests lands on real b-tree content.
func seedRows(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		err := s.WithTx(ctx, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO meta (key, value) VALUES (?, ?)`,
				fmt.Sprintf("durability_test_filler_%d", i),
				fmt.Sprintf("%01024d", i), // 1 KiB per row
			)
			return err
		})
		require.NoError(t, err)
	}
}

// TestOpenDB_DurabilityPragmasExplicit verifies the write connection runs
// with the pragmas NFR-2.8 requires, independent of driver defaults.
func TestOpenDB_DurabilityPragmasExplicit(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	ctx := context.Background()

	var journalMode string
	require.NoError(t, s.writeDB.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journalMode))
	assert.Equal(t, "wal", journalMode)

	var synchronous int
	require.NoError(t, s.writeDB.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous))
	assert.Equal(t, 2, synchronous, "synchronous must be FULL (2), set explicitly")

	var autocheckpoint int
	require.NoError(t, s.writeDB.QueryRowContext(ctx, "PRAGMA wal_autocheckpoint").Scan(&autocheckpoint))
	assert.Equal(t, 1000, autocheckpoint)
}

// TestValidateDBFile_FreshAndHealthy: a missing file is a fresh DB and a
// cleanly closed DB passes validation.
func TestValidateDBFile_FreshAndHealthy(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, validateDBFile(filepath.Join(dir, "absent.db")))

	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())
	require.NoError(t, validateDBFile(dbPath))
}

// doctorHeaderPageCount rewrites the in-header page count (offset 28) so
// the header claims more pages than the file holds — the exact signature
// of the 2026-05-19 torn-checkpoint incident — while keeping
// version-valid-for (offset 92) in agreement with the change counter
// (offset 24) so the count is authoritative.
func doctorHeaderPageCount(t *testing.T, dbPath string, pages uint32) {
	t.Helper()
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	require.NoError(t, err)
	defer func() { require.NoError(t, f.Close()) }()

	header := make([]byte, 100)
	_, err = f.ReadAt(header, 0)
	require.NoError(t, err)

	binary.BigEndian.PutUint32(header[28:32], pages)
	copy(header[92:96], header[24:28]) // version-valid-for = change counter

	_, err = f.WriteAt(header, 0)
	require.NoError(t, err)
}

// TestValidateDBFile_TruncatedNoWAL: header claims more pages than the
// file holds and no WAL exists — must refuse with ErrCorrupted before any
// connection touches the file.
func TestValidateDBFile_TruncatedNoWAL(t *testing.T) {
	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())

	doctorHeaderPageCount(t, dbPath, 1<<20) // claim 4 GiB worth of pages
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	err := validateDBFile(dbPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrCorrupted)
	assert.Contains(t, err.Error(), "truncated")

	// New() must refuse the same way, without creating a WAL.
	_, err = New(dbPath, slog.Default())
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrCorrupted)
	_, statErr := os.Stat(dbPath + "-wal")
	assert.True(t, os.IsNotExist(statErr),
		"refusing to open must not create or touch a WAL on the damaged file")
}

// TestValidateDBFile_TruncatedWithWAL: with a non-empty WAL alongside, the
// truncation may be repairable by WAL replay — validation must defer to
// SQLite instead of fail-stopping.
func TestValidateDBFile_TruncatedWithWAL(t *testing.T) {
	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())

	doctorHeaderPageCount(t, dbPath, 1<<20)
	require.NoError(t, os.WriteFile(dbPath+"-wal", []byte("frames"), 0o644))

	require.NoError(t, validateDBFile(dbPath))
}

// TestValidateDBFile_SubHeaderFile: a non-empty file smaller than the
// 100-byte SQLite header can never be a valid database.
func TestValidateDBFile_SubHeaderFile(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "mtix.db")
	require.NoError(t, os.WriteFile(dbPath, []byte("stub"), 0o644))

	err := validateDBFile(dbPath)
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrCorrupted)
}

// TestNew_QuickCheckCatchesMidFileCorruption: header-intact damage (a
// shredded interior page) is invisible to the O(1) header check but must
// be caught by quick_check on open, before any write.
func TestNew_QuickCheckCatchesMidFileCorruption(t *testing.T) {
	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	// Shred page 3 (offset 2*4096), leaving the header valid.
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	require.NoError(t, err)
	garbage := make([]byte, 4096)
	for i := range garbage {
		garbage[i] = 0xFF
	}
	_, err = f.WriteAt(garbage, 2*4096)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	_, err = New(dbPath, slog.Default())
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrCorrupted)
	assert.Contains(t, err.Error(), "integrity check")
}

// TestIntegrityCheckOnOpen_SkipEnv: the documented escape hatch for
// recovery tooling bypasses quick_check.
func TestIntegrityCheckOnOpen_SkipEnv(t *testing.T) {
	t.Setenv(skipIntegrityCheckEnv, "1")

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "any.db"))
	require.NoError(t, err)
	defer func() { _ = db.Close() }()

	// With the env set the check is a no-op regardless of DB state.
	require.NoError(t, integrityCheckOnOpen(context.Background(), db, "any.db"))
}

// TestNew_SkipEnvBypassesIntegrityGates: the escape hatch must let
// recovery commands reach damage that SQLite itself can still open
// (quick_check-class corruption) — otherwise the recovery runbook dead-
// ends at "cannot open". Truncation-class damage stays unopenable because
// SQLite's own WAL machinery rejects it; salvage for that class is
// mtix recover (MTIX-26.5).
func TestNew_SkipEnvBypassesIntegrityGates(t *testing.T) {
	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	// Shred an interior page: header stays valid, quick_check fails.
	f, err := os.OpenFile(dbPath, os.O_RDWR, 0)
	require.NoError(t, err)
	garbage := make([]byte, 4096)
	for i := range garbage {
		garbage[i] = 0xFF
	}
	_, err = f.WriteAt(garbage, 2*4096)
	require.NoError(t, err)
	require.NoError(t, f.Close())

	// Default posture: refused at open.
	_, err = New(dbPath, slog.Default())
	require.ErrorIs(t, err, model.ErrCorrupted)

	// Escape hatch: the open must succeed so salvage tooling (verify,
	// export, backup) can read what remains.
	t.Setenv(skipIntegrityCheckEnv, "1")
	s2, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	require.NoError(t, s2.Close())
}

// TestNew_SkipEnvOnTruncatedDB: on truncation-class damage the hatch
// bypasses OUR refusal, and the open then fails inside SQLite itself —
// the pre-existing behavior, with no evidence destroyed. There must be no
// "is truncated" refusal in that path.
func TestNew_SkipEnvOnTruncatedDB(t *testing.T) {
	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())

	doctorHeaderPageCount(t, dbPath, 1<<20)
	_ = os.Remove(dbPath + "-wal")
	_ = os.Remove(dbPath + "-shm")

	t.Setenv(skipIntegrityCheckEnv, "1")
	_, err := New(dbPath, slog.Default())
	require.Error(t, err, "SQLite itself cannot open a file truncated this deeply")
	assert.NotContains(t, err.Error(), "is truncated",
		"the hatch must bypass mtix's own refusal")
	assert.NotErrorIs(t, err, model.ErrCorrupted)
}

// TestNextSequence_GuardedByPreflight: NextSequence writes outside WithTx
// and must still honor the free-space floor (NFR-2.8).
func TestNextSequence_GuardedByPreflight(t *testing.T) {
	t.Setenv(minFreeBytesEnv, "18446744073709551615")
	s, _ := newDurabilityTestStore(t)

	_, err := s.NextSequence(context.Background(), "T:")
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrDiskFull)
}

// TestWithTx_PreflightRefusesBelowFloor: with the floor raised above any
// real volume's free space, writes are refused with ErrDiskFull before a
// transaction begins; reads keep working.
func TestWithTx_PreflightRefusesBelowFloor(t *testing.T) {
	t.Setenv(minFreeBytesEnv, "18446744073709551615") // max uint64
	s, _ := newDurabilityTestStore(t)
	ctx := context.Background()

	err := s.WithTx(ctx, func(tx *sql.Tx) error {
		t.Fatal("transaction body must not run when pre-flight refuses")
		return nil
	})
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrDiskFull)

	var n int
	require.NoError(t, s.readDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM meta").Scan(&n),
		"reads must keep working when writes are refused")
}

// TestWithTx_PreflightDisabledByZeroFloor: floor 0 disables the check.
func TestWithTx_PreflightDisabledByZeroFloor(t *testing.T) {
	t.Setenv(minFreeBytesEnv, "0")
	s, _ := newDurabilityTestStore(t)

	err := s.WithTx(context.Background(), func(tx *sql.Tx) error { return nil })
	require.NoError(t, err)
}

// TestWithTx_FatalErrorLatchesFailStop: a write error that classifies as
// fatal I/O latches fail-stop, and every subsequent write is refused with
// the original cause.
func TestWithTx_FatalErrorLatchesFailStop(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	ctx := context.Background()

	fatal := errors.New("database or disk is full (13)")
	err := s.WithTx(ctx, func(tx *sql.Tx) error { return fatal })
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail-stop")

	err = s.WithTx(ctx, func(tx *sql.Tx) error {
		t.Fatal("no transaction may start after fail-stop latched")
		return nil
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "fail-stop")
	assert.ErrorIs(t, err, fatal, "the original cause must stay visible")
}

// TestWithTx_OrdinaryErrorDoesNotLatch: domain errors roll back the
// transaction but leave the store healthy.
func TestWithTx_OrdinaryErrorDoesNotLatch(t *testing.T) {
	s, _ := newDurabilityTestStore(t)
	ctx := context.Background()

	domainErr := fmt.Errorf("node X: %w", model.ErrNotFound)
	err := s.WithTx(ctx, func(tx *sql.Tx) error { return domainErr })
	require.ErrorIs(t, err, model.ErrNotFound)

	require.NoError(t, s.WithTx(ctx, func(tx *sql.Tx) error { return nil }),
		"a domain error must not poison later writes")
}

// TestIsFatalIOError classifies the SQLite result codes that mean the
// filesystem failed underneath us — and nothing else.
func TestIsFatalIOError(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		fatal bool
	}{
		{"nil", nil, false},
		{"sqlite full", errors.New("database or disk is full (13)"), true},
		{"sqlite ioerr", errors.New("disk I/O error (10)"), true},
		{"sqlite corrupt", errors.New("database disk image is malformed (11)"), true},
		{"wrapped enospc", fmt.Errorf("write: %w", syscall.ENOSPC), true},
		{"wrapped eio", fmt.Errorf("write: %w", syscall.EIO), true},
		{"busy", errors.New("database is locked (5) (SQLITE_BUSY)"), false},
		{"domain", model.ErrNotFound, false},
		{"constraint", errors.New("UNIQUE constraint failed: nodes.id"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.fatal, isFatalIOError(tc.err))
		})
	}
}

// TestBackup_PreflightRefusesBelowFloor: VACUUM INTO is refused when the
// destination volume cannot hold the copy.
func TestBackup_PreflightRefusesBelowFloor(t *testing.T) {
	t.Setenv(minFreeBytesEnv, "18446744073709551615")
	s, _ := newDurabilityTestStore(t)

	_, err := s.Backup(context.Background(), filepath.Join(t.TempDir(), "backup.db"))
	require.Error(t, err)
	assert.ErrorIs(t, err, model.ErrDiskFull)
}

// TestQuickCheckOnOpen_Latency keeps the open-time integrity check honest
// about its cost: it must stay well under interactive budgets at a
// CLI-realistic database size.
func TestQuickCheckOnOpen_Latency(t *testing.T) {
	if testing.Short() {
		t.Skip("latency measurement skipped in -short")
	}
	s, dbPath := newDurabilityTestStore(t)
	seedRows(t, s)
	require.NoError(t, s.Close())

	start := time.Now()
	s2, err := New(dbPath, slog.Default())
	require.NoError(t, err)
	defer func() { _ = s2.Close() }()
	elapsed := time.Since(start)

	assert.Less(t, elapsed, 2*time.Second,
		"open including quick_check must stay interactive (got %v)", elapsed)
}
