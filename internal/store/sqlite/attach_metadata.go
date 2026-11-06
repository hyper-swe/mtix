// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// StoreInstanceID returns the creation identity, or empty for an unenrolled legacy store.
func (s *Store) StoreInstanceID(ctx context.Context) (string, error) {
	value, _, err := s.readAttachMeta(ctx, "store_instance_id")
	return value, err
}

// MinDispatchProtocol returns the local dispatch protocol floor, or zero when unset.
func (s *Store) MinDispatchProtocol(ctx context.Context) (int, error) {
	value, found, err := s.readAttachMeta(ctx, "min_dispatch_protocol")
	if err != nil || !found {
		return 0, err
	}
	protocol, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("parse dispatch protocol: %w", err)
	}
	if protocol < 0 {
		return 0, errors.New("dispatch protocol must be nonnegative")
	}
	return protocol, nil
}

// SetMinDispatchProtocol persists the local dispatch protocol floor.
func (s *Store) SetMinDispatchProtocol(ctx context.Context, protocol int) error {
	if protocol < 0 {
		return errors.New("dispatch protocol must be nonnegative")
	}
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO meta(key,value) VALUES('min_dispatch_protocol',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", strconv.Itoa(protocol))
		if err != nil {
			return fmt.Errorf("write dispatch protocol: %w", err)
		}
		return nil
	})
}

// readAttachMeta preserves the distinction between missing metadata and failed reads.
func (s *Store) readAttachMeta(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := s.readDB.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("read attach metadata: %w", err)
	}
	return value, true, nil
}

// The identity read tries a few times, a short pause apart (three tries,
// 30 ms of waiting at most), because a lone side file or a busy result is seen
// while another process opens or closes the store and clears within moments.
// The bound is small: a hook waits for it.
const (
	readAttempts = 3
	retryPause   = 15 * time.Millisecond
)

// errTransientRead marks a read failure that may clear if tried again.
var errTransientRead = errors.New("transient store read")

// openStoreReadOnly opens the database file at dbPath so that nothing can be
// written through it and a missing file is never created (MTIX-112.19.8).
func openStoreReadOnly(dbPath string) (*sql.DB, error) {
	return openSQLite(dbPath, readOnlyDSNParams()...)
}

// ReadStoreInstanceIDFile returns store_instance_id from the database file at
// dbPath without creating any file, not even the WAL and shm side files a
// read-only SQLite open of a WAL database would create (a store in use may
// update its own shm content), for callers
// that must not open a Store (and so must not migrate, create or write). The
// result is empty for a legacy store with no identity. A missing, empty or
// non-database file is an error, never a new database. A store with no side
// files is fully contained in the database file and is read immutable (no
// locks, no side files); a store in use (WAL and shm both present) is read
// read-only through them; a WAL without its shm, or the reverse, cannot be
// read without creating a file, so it is refused and the caller declines. A
// lone side file or a busy result is tried again (readAttempts tries,
// retryPause apart) before it is refused, since another process opening or
// closing the store clears it within moments (MTIX-112.19.8).
func ReadStoreInstanceIDFile(ctx context.Context, dbPath string) (string, error) {
	var lastErr error
	for attempt := 0; attempt < readAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", fmt.Errorf("read store identity: %w", ctx.Err())
			case <-time.After(retryPause):
			}
		}
		id, err := readStoreInstanceIDOnce(ctx, dbPath)
		if err == nil || !errors.Is(err, errTransientRead) {
			return id, err
		}
		lastErr = err
	}
	return "", lastErr
}

// readStoreInstanceIDOnce is one try of ReadStoreInstanceIDFile. An error that
// may clear on its own (a lone side file, a busy store) wraps errTransientRead.
func readStoreInstanceIDOnce(ctx context.Context, dbPath string) (id string, retErr error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		return "", fmt.Errorf("read store identity: %w", err)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("read store identity: %s is 0 bytes, not a database", dbPath)
	}
	params, err := sideFileFreeParams(dbPath)
	if err != nil {
		return "", err
	}
	db, err := openSQLite(dbPath, params...)
	if err != nil {
		return "", fmt.Errorf("open store read-only: %w", err)
	}
	defer func() {
		if closeErr := db.Close(); retErr == nil && closeErr != nil {
			id, retErr = "", fmt.Errorf("close store read-only: %w", closeErr)
		}
	}()
	var value string
	err = db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", "store_instance_id").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", transientIfBusy(fmt.Errorf("read store identity: %w", err))
	}
	return value, nil
}

// transientIfBusy marks err as retryable when SQLite reports the store busy or
// locked (another process is opening or closing it).
func transientIfBusy(err error) error {
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "sqlite_busy") || strings.Contains(msg, "database is locked") || strings.Contains(msg, "sqlite_locked") {
		return fmt.Errorf("%w: %w", errTransientRead, err)
	}
	return err
}

// sideFileFreeParams picks the read-only DSN parameters that cannot create a
// file next to the database at dbPath (see ReadStoreInstanceIDFile).
func sideFileFreeParams(dbPath string) ([]string, error) {
	wal, err := sideFilePresent(dbPath + "-wal")
	if err != nil {
		return nil, err
	}
	shm, err := sideFilePresent(dbPath + "-shm")
	if err != nil {
		return nil, err
	}
	switch {
	case !wal && !shm:
		return append(readOnlyDSNParams(), "immutable=1"), nil
	case wal && shm:
		return readOnlyDSNParams(), nil
	}
	return nil, fmt.Errorf("%w: read store identity: %s has a WAL or shm file without the other, which cannot be read without creating a file", errTransientRead, dbPath)
}

// sideFilePresent reports whether the side file at path exists.
func sideFilePresent(path string) (bool, error) {
	if _, err := os.Lstat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("read store identity: %w", err)
	}
	return true, nil
}
