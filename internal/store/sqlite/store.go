// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Package sqlite implements the Store interface using pure Go SQLite
// via modernc.org/sqlite per NFR-2.1 and ADR-001.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	// Pure Go SQLite driver — no CGO (NFR-4.7).
	_ "modernc.org/sqlite"

	"github.com/hyper-swe/mtix/internal/model"
)

// resolveDBPath returns a file path for the SQLite database.
// If path is an existing directory, it appends "mtix.db".
// Otherwise it returns path unchanged (assumed to be a file path).
func resolveDBPath(path string) string {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return filepath.Join(path, "mtix.db")
	}
	return path
}

// Store implements store.Store using SQLite with WAL mode.
// It uses separate read and write connection pools per NFR-2.1.
type Store struct {
	writeDB *sql.DB
	readDB  *sql.DB
	logger  *slog.Logger
	clock   func() time.Time
}

// New creates a new Store with the given database path.
// If dbPath is a directory, the database file is created as mtix.db inside it.
// It opens separate read and write connections, enables WAL mode,
// foreign keys, and sets busy timeout per NFR-2.1.
func New(dbPath string, logger *slog.Logger) (*Store, error) {
	dbPath = resolveDBPath(dbPath)
	ctx := context.Background()
	writeDB, err := openDB(ctx, dbPath, true)
	if err != nil {
		return nil, fmt.Errorf("open write db %s: %w", dbPath, err)
	}

	readDB, err := openDB(ctx, dbPath, false)
	if err != nil {
		if closeErr := writeDB.Close(); closeErr != nil {
			logger.Error("failed to close write db after read db open failure",
				slog.Any("error", closeErr))
		}
		return nil, fmt.Errorf("open read db %s: %w", dbPath, err)
	}

	s := &Store{
		writeDB: writeDB,
		readDB:  readDB,
		logger:  logger,
		clock:   func() time.Time { return time.Now().UTC() },
	}

	if err := s.init(context.Background()); err != nil {
		if closeErr := s.Close(); closeErr != nil {
			logger.Error("failed to close store after init failure",
				slog.Any("error", closeErr))
		}
		return nil, fmt.Errorf("init store: %w", err)
	}

	return s, nil
}

// openDB creates a database connection with appropriate settings.
// Writer connections use BEGIN IMMEDIATE via _txlock=immediate DSN parameter,
// ensuring busy_timeout is always applied (BEGIN DEFERRED bypasses it on
// lock upgrade). Both read and write connections set busy_timeout = 5000ms
// to prevent immediate failures during concurrent access.
func openDB(ctx context.Context, path string, isWriter bool) (*sql.DB, error) {
	dsn := path
	if isWriter {
		dsn = path + "?_txlock=immediate"
	}

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sql.Open: %w", err)
	}

	// PRAGMA foreign_keys = ON — required on ALL connections (NFR-2.1).
	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("close after pragma failure: %w", closeErr)
		}
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	// Busy timeout on ALL connections — prevents immediate SQLITE_BUSY failures
	// during concurrent access from multiple processes or agents.
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		if closeErr := db.Close(); closeErr != nil {
			return nil, fmt.Errorf("close after busy_timeout failure: %w", closeErr)
		}
		return nil, fmt.Errorf("set busy_timeout: %w", err)
	}

	if isWriter {
		// Serialized writes — one connection (NFR-2.1).
		db.SetMaxOpenConns(1)

		// WAL mode for concurrent readers (NFR-2.1).
		if _, err := db.ExecContext(ctx, "PRAGMA journal_mode = WAL"); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				return nil, fmt.Errorf("close after wal failure: %w", closeErr)
			}
			return nil, fmt.Errorf("enable WAL: %w", err)
		}
	}

	return db, nil
}

// init creates the database schema if it doesn't exist and runs any
// needed forward migrations. Refuses to start if the database was created
// by a newer schema version per NFR-2.6.
//
// Dispatch order (within one transaction per migration step so a crash
// leaves the DB at a known version, never half-migrated):
//  1. Read existing meta.schema_version (0 = fresh DB / no meta table yet).
//  2. If existing == 1: run migrateV1ToV2SQL (drops legacy sync_events).
//  3. Run schemaSQL (idempotent CREATE IF NOT EXISTS for every table;
//     INSERT OR IGNORE for every meta key).
//  4. UPDATE meta.schema_version to current schemaVersion if it had been
//     at an older version. INSERT OR IGNORE in step 3 only sets the row
//     for fresh DBs; an existing v1 row needs an explicit bump.
//  5. Refuse if existing > schemaVersion.
func (s *Store) init(ctx context.Context) error {
	existingVersion, err := s.readExistingSchemaVersion(ctx)
	if err != nil {
		return err
	}

	if existingVersion > schemaVersion {
		return fmt.Errorf(
			"database schema version %d is newer than supported version %d: %w",
			existingVersion, schemaVersion, model.ErrConflict,
		)
	}

	if existingVersion == 1 {
		if _, err := s.writeDB.ExecContext(ctx, migrateV1ToV2SQL); err != nil {
			return fmt.Errorf("migrate v1 -> v2 (drop legacy sync_events): %w", err)
		}
		s.logger.Info("schema_migrated",
			"event", "schema_migrated",
			"from_version", 1,
			"to_version", 2,
			"dropped_tables", "sync_events_v1",
		)
	}

	if _, err := s.writeDB.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	if existingVersion > 0 && existingVersion < schemaVersion {
		_, err := s.writeDB.ExecContext(ctx,
			`UPDATE meta SET value = ? WHERE key = 'schema_version'`,
			fmt.Sprintf("%d", schemaVersion),
		)
		if err != nil {
			return fmt.Errorf("update schema_version after migration: %w", err)
		}
	}

	return nil
}

// readExistingSchemaVersion returns the meta.schema_version recorded in
// the DB before init runs. Returns 0 if the meta table or the row is
// absent (fresh DB).
func (s *Store) readExistingSchemaVersion(ctx context.Context) (int, error) {
	// Probe for the meta table first; querying a missing table errors.
	var tableName string
	err := s.readDB.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name='meta'`,
	).Scan(&tableName)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("probe meta table: %w", err)
	}

	var versionStr string
	err = s.readDB.QueryRowContext(ctx,
		`SELECT value FROM meta WHERE key = 'schema_version'`,
	).Scan(&versionStr)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}

	var version int
	if _, err := fmt.Sscanf(versionStr, "%d", &version); err != nil {
		return 0, fmt.Errorf("parse schema version %q: %w", versionStr, err)
	}
	return version, nil
}

// SetClock overrides the clock function used by the store.
// Intended for testing to inject deterministic timestamps.
func (s *Store) SetClock(clock func() time.Time) {
	s.clock = clock
}

// Close closes both the read and write database connections.
func (s *Store) Close() error {
	var firstErr error

	if err := s.writeDB.Close(); err != nil {
		firstErr = fmt.Errorf("close write db: %w", err)
	}

	if err := s.readDB.Close(); err != nil {
		if firstErr == nil {
			firstErr = fmt.Errorf("close read db: %w", err)
		}
	}

	return firstErr
}

// NextSequence atomically increments and returns the next sequence per FR-2.7.
// Uses INSERT ... ON CONFLICT DO UPDATE SET value = value + 1 RETURNING value
// for atomic, collision-free sequence generation.
// Key format: '{project}:{parent_dotpath}' (e.g., 'PROJ:', 'PROJ:PROJ-42.1').
func (s *Store) NextSequence(ctx context.Context, key string) (int, error) {
	var value int

	// Atomic upsert per FR-2.7 — parameterized query, no string concatenation.
	err := s.writeDB.QueryRowContext(ctx,
		`INSERT INTO sequences (key, value) VALUES (?, 1)
		 ON CONFLICT(key) DO UPDATE SET value = value + 1
		 RETURNING value`,
		key,
	).Scan(&value)
	if err != nil {
		return 0, fmt.Errorf("next sequence for %s: %w", key, err)
	}

	return value, nil
}

// UpdateProgress sets the progress value for a node.
func (s *Store) UpdateProgress(ctx context.Context, id string, progress float64) error {
	return s.WithTx(ctx, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx,
			`UPDATE nodes SET progress = ?, updated_at = ?
			 WHERE id = ? AND deleted_at IS NULL`,
			progress, time.Now().UTC().Format(time.RFC3339), id,
		)
		if err != nil {
			return fmt.Errorf("update progress for %s: %w", id, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("check rows affected: %w", err)
		}
		if affected == 0 {
			return fmt.Errorf("node %s: %w", id, model.ErrNotFound)
		}
		return nil
	})
}
