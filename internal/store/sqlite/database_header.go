// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"encoding/binary"
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/model"
)

// databaseHeaderFile is the narrow seam validateOpenDBFile reads through. It
// exists only so tests can inject a deterministic file-growth interleaving
// (a real os.File satisfies it in production); no behavior is swapped.
type databaseHeaderFile interface {
	ReadAt([]byte, int64) (int, error)
	Stat() (os.FileInfo, error)
}

// validateOpenDBFile checks one open file, allowing metadata/read interleavings
// to be exercised without timing-dependent sleeps.
func validateOpenDBFile(path string, f databaseHeaderFile) error {
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat database header %s: %w", path, err)
	}
	size := info.Size()
	if size == 0 {
		return nil
	}
	if size < 100 {
		return fmt.Errorf("database file %s is %d bytes, smaller than a SQLite header; %s: %w",
			path, size, recoveryGuidance, model.ErrCorrupted)
	}

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

	return validateDBPageCount(path, f, pageCount, pageSize)
}

// validateDBPageCount observes WAL absence before measuring the main file. A
// concurrent checkpoint may grow the main file and then remove WAL; measuring
// in the reverse order combines two states into a false truncation diagnosis.
func validateDBPageCount(path string, f databaseHeaderFile, pageCount, pageSize uint64) error {
	if walInfo, walErr := os.Stat(path + "-wal"); walErr == nil && walInfo.Size() > 0 {
		return nil
	}
	info, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat database after header %s: %w", path, err)
	}
	size := info.Size()
	// size >= 0 is always true for a regular file; it bounds the int64 to
	// uint64 conversion for gosec G115.
	if size >= 0 && pageCount*pageSize <= uint64(size) {
		return nil
	}

	return fmt.Errorf(
		"database file %s is truncated: header records %d pages of %d bytes (%d bytes total) but the file is %d bytes, and no WAL exists to repair it; %s: %w",
		path, pageCount, pageSize, pageCount*pageSize, size, recoveryGuidance, model.ErrCorrupted)
}
