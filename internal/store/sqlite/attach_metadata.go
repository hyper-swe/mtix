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

// ReadStoreInstanceIDFile returns store_instance_id from the database file at
// dbPath, opened read-only, for callers that must not open a Store (and so must
// not migrate, create or write anything). The result is empty for a legacy
// store with no identity. A missing, empty or non-database file is an error,
// never a new database (MTIX-112.19.8).
func ReadStoreInstanceIDFile(ctx context.Context, dbPath string) (string, error) {
	info, err := os.Stat(dbPath)
	if err != nil {
		return "", fmt.Errorf("read store identity: %w", err)
	}
	if info.Size() == 0 {
		return "", fmt.Errorf("read store identity: %s is 0 bytes, not a database", dbPath)
	}
	db, err := openSQLite(dbPath, readOnlyDSNParams()...)
	if err != nil {
		return "", fmt.Errorf("open store read-only: %w", err)
	}
	defer func() { _ = db.Close() }()
	var value string
	err = db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key=?", "store_instance_id").Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read store identity: %w", err)
	}
	return value, nil
}
