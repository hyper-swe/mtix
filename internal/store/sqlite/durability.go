// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	sqlite3 "modernc.org/sqlite"

	"github.com/hyper-swe/mtix/internal/model"
)

// Durability controls per NFR-2.8 (disk-full safety).
//
// The storage layer refuses to start or continue work it cannot finish:
//   - validateDBFile runs BEFORE the first connection is opened, so a
//     truncated main file is detected without disturbing any WAL that
//     could still repair it.
//   - quick_check runs on open, before any write, so corruption is
//     surfaced at the earliest safe moment instead of mid-mutation.
//   - preflightWrite refuses transactions when free disk space is below
//     minFreeBytes, because a write that fails halfway (ENOSPC during a
//     WAL checkpoint) can tear the database.
//   - After any fatal I/O error the store latches into fail-stop: every
//     subsequent write is refused. Continuing into undefined state is
//     never an option.

const (
	// defaultMinFreeBytes is the free-space floor below which writes are
	// refused. Sized to cover a full autocheckpoint backfill
	// (wal_autocheckpoint = 1000 pages x 4 KiB = 4 MiB) twice over.
	defaultMinFreeBytes uint64 = 8 << 20

	// minFreeBytesEnv overrides defaultMinFreeBytes (integer, bytes).
	// Setting it to 0 disables the pre-flight check.
	minFreeBytesEnv = "MTIX_MIN_FREE_BYTES"

	// skipIntegrityCheckEnv disables BOTH open-time integrity gates (the
	// pre-open truncation validation and the quick_check) when set to
	// "1". Escape hatch so recovery commands — verify, backup, export —
	// can still reach a known-damaged database; without it, a corrupted
	// file would lock users out of the very tools the recovery runbook
	// tells them to use. Documented in USERMANUAL troubleshooting.
	skipIntegrityCheckEnv = "MTIX_SKIP_INTEGRITY_CHECK"

	// recoveryGuidance is appended to corruption errors so the failure
	// is actionable at the moment it is seen.
	recoveryGuidance = "the database failed an integrity check; do NOT keep writing. " +
		"Restore from a backup (mtix backup snapshots or .mtix/tasks.json via " +
		"'mtix import'), and see USERMANUAL 'Corruption recovery' before deleting any file"
)

// integrityChecksSkipped reports whether the recovery escape hatch is on.
func integrityChecksSkipped() bool {
	return os.Getenv(skipIntegrityCheckEnv) == "1"
}

// minFreeBytes resolves the free-space floor, honoring the env override.
func minFreeBytes() uint64 {
	if v := os.Getenv(minFreeBytesEnv); v != "" {
		if n, err := strconv.ParseUint(v, 10, 64); err == nil {
			return n
		}
	}
	return defaultMinFreeBytes
}

// validateDBFile detects main-file truncation BEFORE any connection is
// opened. A SQLite header whose page count implies a size larger than the
// file on disk is the signature of a torn checkpoint (observed in the
// 2026-05-19 incident: header claimed 136 pages, file held 72).
//
// If a non-empty WAL sits next to the database the check passes: SQLite
// can replay the WAL on open and repair the main file, and refusing to
// open would block exactly that self-healing. Only when no WAL can help
// (absent or zero bytes) is the truncation unrecoverable-in-place, and
// opening it — which may reset the WAL and checkpoint garbage — would
// destroy evidence. Fail stop instead.
func validateDBFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		// Missing file is a fresh database, not corruption.
		return nil //nolint:nilerr // absence is the legitimate fresh-DB case
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	if size < 100 {
		return fmt.Errorf("database file %s is %d bytes, smaller than a SQLite header; %s: %w",
			path, size, recoveryGuidance, model.ErrCorrupted)
	}

	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open database header %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	header := make([]byte, 100)
	if _, err := f.ReadAt(header, 0); err != nil {
		return fmt.Errorf("read database header %s: %w", path, err)
	}
	if string(header[:16]) != "SQLite format 3\x00" {
		return fmt.Errorf("database file %s does not have a SQLite header; %s: %w",
			path, recoveryGuidance, model.ErrCorrupted)
	}

	pageSize := uint64(binary.BigEndian.Uint16(header[16:18]))
	if pageSize == 1 {
		pageSize = 65536
	}
	pageCount := uint64(binary.BigEndian.Uint32(header[28:32]))
	changeCounter := binary.BigEndian.Uint32(header[24:28])
	versionValidFor := binary.BigEndian.Uint32(header[92:96])

	// The in-header page count is only authoritative when
	// version-valid-for matches the change counter (SQLite file format
	// §1.3.7). When they differ, a legacy writer left the count stale —
	// fall through and let quick_check judge.
	if versionValidFor != changeCounter {
		return nil
	}

	if pageCount*pageSize <= uint64(size) {
		return nil
	}

	// Header claims more pages than the file holds. Recoverable only if
	// a WAL with content exists for SQLite to replay.
	if walInfo, walErr := os.Stat(path + "-wal"); walErr == nil && walInfo.Size() > 0 {
		return nil
	}

	return fmt.Errorf(
		"database file %s is truncated: header records %d pages of %d bytes (%d bytes total) but the file is %d bytes, and no WAL exists to repair it; %s: %w",
		path, pageCount, pageSize, pageCount*pageSize, size, recoveryGuidance, model.ErrCorrupted)
}

// quickCheck runs PRAGMA quick_check(1) and returns its first result row.
// quick_check skips index-content verification so it stays fast for
// CLI-scale databases; structural damage (torn pages, broken b-trees) is
// exactly what it catches. Shared by open-time validation and backup
// verification.
func quickCheck(ctx context.Context, db *sql.DB) (string, error) {
	var result string
	err := db.QueryRowContext(ctx, "PRAGMA quick_check(1)").Scan(&result)
	return result, err
}

// integrityCheckOnOpen runs quickCheck on the freshly opened write
// connection, before init or any mutation.
func integrityCheckOnOpen(ctx context.Context, db *sql.DB, path string) error {
	if integrityChecksSkipped() {
		return nil
	}

	result, err := quickCheck(ctx, db)
	if err != nil {
		return fmt.Errorf("integrity check on %s could not run; %s: %w (%v)",
			path, recoveryGuidance, model.ErrCorrupted, err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check on %s failed (%s); %s: %w",
			path, result, recoveryGuidance, model.ErrCorrupted)
	}
	return nil
}

// explainOpenError enriches a database-open failure with free-space
// context. On a packed volume the very first failure is SQLITE_CANTOPEN
// while creating the -wal file — an error that says nothing about disk
// space. Naming the real cause here is the difference between a user
// freeing space and a user assuming their database is broken.
func explainOpenError(err error, dbPath string) error {
	if err == nil {
		return nil
	}
	// Enrich whenever the volume is below the floor — or nearly empty in
	// absolute terms, so a disabled floor (MTIX_MIN_FREE_BYTES=0) still
	// gets a truthful diagnosis.
	threshold := max(minFreeBytes(), 1<<20)
	free, statErr := freeDiskSpace(filepath.Dir(dbPath))
	if statErr == nil && free < threshold {
		return fmt.Errorf(
			"cannot open database %s: the volume has only %d bytes free, which is likely the cause — free disk space and retry: %w (%v)",
			dbPath, free, model.ErrDiskFull, err)
	}
	return err
}

// preflightWrite refuses a write before it starts when the store has
// latched fail-stop or the volume is too full to finish safely.
func (s *Store) preflightWrite() error {
	// MTIX-58: an unsafe-filesystem store is read-only — refuse every write with
	// no override, before touching the DB.
	if s.writeRefused {
		return writeRefusedError(s.dbPath, s.fsType)
	}
	if cause := s.failStopCause(); cause != nil {
		return fmt.Errorf("store is in fail-stop after a fatal storage error and refuses further writes (restart after freeing disk space / restoring): %w", cause)
	}

	floor := s.minFreeBytes
	if floor == 0 {
		return nil
	}
	free, err := freeDiskSpace(s.dbDir)
	if err != nil {
		// Pre-flight must never turn a healthy system read-only because
		// statfs is unsupported; log and proceed.
		s.logger.Warn("free-space pre-flight unavailable", "dir", s.dbDir, "error", err)
		return nil
	}
	if free < floor {
		return fmt.Errorf(
			"refusing write: only %d bytes free on the volume holding %s (floor %d bytes, override with %s); free disk space and retry: %w",
			free, s.dbDir, floor, minFreeBytesEnv, model.ErrDiskFull)
	}
	return nil
}

// preflightBackup refuses a backup when the destination volume cannot hold
// a full copy of the database (DB + WAL + the configured floor).
func (s *Store) preflightBackup(destPath string) error {
	if cause := s.failStopCause(); cause != nil {
		// Backups of a fail-stopped store are still allowed — getting a
		// copy off the failing volume is the recovery move — but only
		// to a DIFFERENT directory than the live database.
		if filepath.Dir(destPath) == s.dbDir {
			return fmt.Errorf("store is in fail-stop; back up to a different volume than the database directory: %w", cause)
		}
	}

	floor := s.minFreeBytes
	if floor == 0 {
		return nil
	}

	need := floor
	for _, p := range []string{filepath.Join(s.dbDir, "mtix.db"), filepath.Join(s.dbDir, "mtix.db-wal")} {
		if info, err := os.Stat(p); err == nil {
			size := uint64(info.Size())     //nolint:gosec // file sizes are never negative
			if size > math.MaxUint64-need { // saturate instead of wrapping
				need = math.MaxUint64
				break
			}
			need += size
		}
	}

	destDir := filepath.Dir(destPath)
	free, err := freeDiskSpace(destDir)
	if err != nil {
		s.logger.Warn("free-space pre-flight unavailable for backup", "dir", destDir, "error", err)
		return nil
	}
	if free < need {
		return fmt.Errorf(
			"refusing backup: destination volume at %s has %d bytes free but the backup needs about %d bytes; free disk space or choose another volume: %w",
			destDir, free, need, model.ErrDiskFull)
	}
	return nil
}

// markFailStop latches the store into fail-stop with the originating error.
// First writer wins; later calls keep the original cause.
func (s *Store) markFailStop(cause error) {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	if s.failCause == nil {
		s.failCause = cause
		s.logger.Error("storage fail-stop latched: refusing all further writes",
			"cause", cause)
	}
}

// failStopCause returns the latched fatal error, or nil if healthy.
func (s *Store) failStopCause() error {
	s.failMu.Lock()
	defer s.failMu.Unlock()
	return s.failCause
}

// classifyWriteError inspects an error from a write path. Fatal I/O
// failures (disk full, I/O error, detected corruption) latch fail-stop and
// are wrapped with fail-stop context; everything else passes through.
func (s *Store) classifyWriteError(err error) error {
	if err == nil {
		return nil
	}
	if !isFatalIOError(err) {
		return err
	}
	s.markFailStop(err)
	// Attach the NFR-2.8 exit-code sentinel (MTIX-32) alongside the raw cause
	// (multi-%w) so exitCodeForError maps the fail-stop to its contract code
	// (disk-full=3 / corrupted=4) on every write path — not only the pre-flight
	// floor. The raw error stays in the chain for diagnostics.
	return fmt.Errorf(
		"fatal storage error — mtix stops writing to avoid corrupting the database (fail-stop): %w (%w)",
		err, fatalIOSentinel(err))
}

// fatalIOSentinel maps a fatal storage error to its exit-code contract
// sentinel: detected corruption -> model.ErrCorrupted, otherwise (disk full /
// ENOSPC / generic fatal I/O) -> model.ErrDiskFull. Caller must have already
// confirmed isFatalIOError(err).
func fatalIOSentinel(err error) error {
	if isCorruptionError(err) {
		return model.ErrCorrupted
	}
	return model.ErrDiskFull
}

// isCorruptionError reports whether a fatal storage error is a corruption
// (SQLITE_CORRUPT / malformed image) rather than a disk-space/I/O failure.
func isCorruptionError(err error) bool {
	var sqErr *sqlite3.Error
	if errors.As(err, &sqErr) && sqErr.Code()&0xFF == sqliteCorrupt {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "database disk image is malformed") ||
		strings.Contains(msg, "SQLITE_CORRUPT")
}

// SQLite primary result codes that mean the filesystem failed under us.
const (
	sqliteIOErr   = 10 // SQLITE_IOERR
	sqliteCorrupt = 11 // SQLITE_CORRUPT
	sqliteFull    = 13 // SQLITE_FULL
)

// isFatalIOError reports whether err indicates the filesystem failed under
// SQLite (ENOSPC / EIO / detected corruption). The driver's typed error
// code is authoritative; string matching remains as a fallback for errors
// that cross layers as plain text (mirroring wrapBusyError).
func isFatalIOError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EIO) {
		return true
	}
	var sqErr *sqlite3.Error
	if errors.As(err, &sqErr) {
		// Extended codes carry the primary code in the low byte.
		switch sqErr.Code() & 0xFF {
		case sqliteIOErr, sqliteCorrupt, sqliteFull:
			return true
		}
	}
	msg := err.Error()
	for _, marker := range []string{
		"database or disk is full",
		"disk I/O error",
		"database disk image is malformed",
		"SQLITE_FULL",
		"SQLITE_IOERR",
		"SQLITE_CORRUPT",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}
