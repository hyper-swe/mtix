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
	"sync"
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

	// Durability state per NFR-2.8.
	dbDir        string     // directory holding the DB, for free-space checks
	minFreeBytes uint64     // write pre-flight floor; 0 disables
	failMu       sync.Mutex // guards failCause
	failCause    error      // first fatal storage error; latches fail-stop

	// onCommit callbacks run, in registration order, after every successfully
	// committed write transaction. It is the choke point that lets long-running
	// interfaces (MCP server, HTTP serve) keep the tasks.json mirror current per
	// FR-15.3 — the CLI's PostRun export never fires inside a long-lived process
	// — and (MTIX-53) dispatch hooks host-side. Registered once during wiring,
	// before any traffic, so the slice needs no lock.
	onCommit []func()
}

// SetOnCommit registers fn as the FIRST post-commit callback, replacing any
// previously set via SetOnCommit but preserving callbacks added with
// AddOnCommit. Must be called during process wiring, before concurrent use.
// Every long-running interface (anything that mutates without exiting) MUST
// wire the auto-export path here, or its users lose the FR-15.3 mirror — the
// gap behind the 2026-05-19 data-loss incident.
func (s *Store) SetOnCommit(fn func()) {
	if len(s.onCommit) == 0 {
		s.onCommit = []func(){fn}
		return
	}
	s.onCommit[0] = fn
}

// AddOnCommit appends fn to the post-commit callbacks (MTIX-53), so a server can
// wire hook dispatch alongside the mirror exporter without one replacing the
// other. Must be called during wiring, before concurrent use.
func (s *Store) AddOnCommit(fn func()) {
	if len(s.onCommit) == 0 {
		// Reserve slot 0 for SetOnCommit so a later SetOnCommit does not
		// displace this callback.
		s.onCommit = []func(){nil}
	}
	s.onCommit = append(s.onCommit, fn)
}

// New creates a new Store with the given database path.
// If dbPath is a directory, the database file is created as mtix.db inside it.
// It opens separate read and write connections, enables WAL mode,
// foreign keys, and sets busy timeout per NFR-2.1.
//
// Per NFR-2.8 the file is validated for truncation BEFORE the first
// connection is opened (opening a torn database can reset the WAL that
// would otherwise repair it), and quick_check runs before any write.
func New(dbPath string, logger *slog.Logger) (*Store, error) {
	dbPath = resolveDBPath(dbPath)
	ctx := context.Background()

	if integrityChecksSkipped() {
		// Recovery escape hatch: verify/backup/export must be able to
		// reach a damaged database, or the recovery runbook is a dead
		// end. Loud by design — this must never run unnoticed.
		logger.Error("DANGER: open-time integrity checks bypassed (" +
			skipIntegrityCheckEnv + "=1); recovery use only — data correctness is not guaranteed")
	} else if err := validateDBFile(dbPath); err != nil {
		return nil, err
	}

	writeDB, err := openDB(ctx, dbPath, true)
	if err != nil {
		return nil, explainOpenError(fmt.Errorf("open write db %s: %w", dbPath, err), dbPath)
	}

	if checkErr := integrityCheckOnOpen(ctx, writeDB, dbPath); checkErr != nil {
		if closeErr := writeDB.Close(); closeErr != nil {
			logger.Error("failed to close write db after integrity failure",
				slog.Any("error", closeErr))
		}
		return nil, checkErr
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
		writeDB:      writeDB,
		readDB:       readDB,
		logger:       logger,
		clock:        func() time.Time { return time.Now().UTC() },
		dbDir:        filepath.Dir(dbPath),
		minFreeBytes: minFreeBytes(),
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

	// PRAGMA foreign_keys = ON and busy_timeout on ALL connections
	// (NFR-2.1); busy_timeout prevents immediate SQLITE_BUSY failures
	// during concurrent access from multiple processes or agents.
	type pragma struct{ stmt, what string }
	pragmas := []pragma{
		{"PRAGMA foreign_keys = ON", "enable foreign keys"},
		{"PRAGMA busy_timeout = 5000", "set busy_timeout"},
	}

	if isWriter {
		// Serialized writes — one connection (NFR-2.1).
		db.SetMaxOpenConns(1)

		pragmas = append(pragmas,
			// WAL mode for concurrent readers (NFR-2.1).
			pragma{"PRAGMA journal_mode = WAL", "enable WAL"},
			// synchronous = FULL, set EXPLICITLY per NFR-2.8 / ADR-001 §9.
			// modernc.org/sqlite happens to default to FULL, but the
			// durability posture of the canonical store must never depend
			// on a driver default. FULL fsyncs the WAL on every commit;
			// NORMAL would trade that for speed and may lose the most
			// recent commits on power failure.
			pragma{"PRAGMA synchronous = FULL", "set synchronous"},
			// Explicit autocheckpoint threshold (the SQLite default,
			// pinned per NFR-2.8 so backfill volume — and therefore the
			// free-space pre-flight floor — is a known quantity).
			pragma{"PRAGMA wal_autocheckpoint = 1000", "set wal_autocheckpoint"},
		)
	}

	for _, p := range pragmas {
		if _, err := db.ExecContext(ctx, p.stmt); err != nil {
			if closeErr := db.Close(); closeErr != nil {
				return nil, fmt.Errorf("close after pragma failure: %w", closeErr)
			}
			return nil, fmt.Errorf("%s: %w", p.what, err)
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

	// v2 -> v3 (MTIX-30.1 / ADR-003 §2, §7 Phase 0): add nodes.uid to an
	// existing table BEFORE schemaSQL creates idx_nodes_uid on it.
	if err := s.addUIDColumnPreV3(ctx, existingVersion); err != nil {
		return err
	}

	// v3 -> v4 (MTIX-30.6 / ADR-003 §3, §7 Phase 3): add sync_events.uid to
	// an existing table BEFORE schemaSQL creates idx_sync_events_uid on it.
	if err := s.addSyncEventUIDColumnPreV4(ctx, existingVersion); err != nil {
		return err
	}

	if _, err := s.writeDB.ExecContext(ctx, schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Deterministic UID backfill for pre-v3 rows (after schemaSQL so the
	// uid index exists; idempotent — only fills empty uids).
	if err := s.backfillUIDsPreV3(ctx, existingVersion); err != nil {
		return err
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

// addUIDColumnPreV3 adds nodes.uid to a pre-v3 database (MTIX-30.1).
// No-op on fresh DBs (schemaSQL already has the column) and on v3+. A
// "duplicate column" error from a re-run after an interrupted migration
// is tolerated.
func (s *Store) addUIDColumnPreV3(ctx context.Context, existingVersion int) error {
	if existingVersion == 0 || existingVersion >= 3 {
		return nil
	}
	if _, err := s.writeDB.ExecContext(ctx, migrateV2ToV3SQL); err != nil &&
		!containsString(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate v2 -> v3 (add nodes.uid): %w", err)
	}
	return nil
}

// addSyncEventUIDColumnPreV4 adds sync_events.uid to a pre-v4 database
// (MTIX-30.6 / ADR-003 §3, §7 Phase 3). No-op on fresh DBs (schemaSQL
// already has the column) and on v4+. A "duplicate column" error from a
// re-run after an interrupted migration is tolerated, mirroring
// addUIDColumnPreV3. Legacy rows keep uid NULL; apply falls back to
// node_id for them (dual-carry), so no row backfill is needed here.
func (s *Store) addSyncEventUIDColumnPreV4(ctx context.Context, existingVersion int) error {
	// Only v2/v3 DBs carry a pre-v4 sync_events table that survives into
	// this init and needs the ALTER. A v1 DB has its legacy sync_events
	// DROPped by the v1->v2 step above and then re-created (with uid) by
	// schemaSQL, so it needs no ALTER; a fresh (0) or already-v4 DB needs
	// none either.
	if existingVersion < 2 || existingVersion >= 4 {
		return nil
	}
	if _, err := s.writeDB.ExecContext(ctx, migrateV3ToV4SQL); err != nil &&
		!containsString(err.Error(), "duplicate column name") {
		return fmt.Errorf("migrate v3 -> v4 (add sync_events.uid): %w", err)
	}
	s.logger.Info("schema_migrated",
		"event", "schema_migrated", "from_version", existingVersion, "to_version", 4,
		"added", "sync_events.uid")
	return nil
}

// backfillUIDsPreV3 runs the deterministic UID backfill for a pre-v3
// database (MTIX-30.1 / ADR-003 §7 Phase 0). No-op on fresh DBs and v3+.
func (s *Store) backfillUIDsPreV3(ctx context.Context, existingVersion int) error {
	if existingVersion == 0 || existingVersion >= 3 {
		return nil
	}
	if err := s.BackfillUIDs(ctx); err != nil {
		return fmt.Errorf("migrate v2 -> v3 (backfill uids): %w", err)
	}
	s.logger.Info("schema_migrated",
		"event", "schema_migrated", "from_version", existingVersion, "to_version", 3,
		"added", "nodes.uid")
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
//
// This write runs outside WithTx, so it carries its own NFR-2.8 guards:
// free-space pre-flight before, fail-stop classification after.
func (s *Store) NextSequence(ctx context.Context, key string) (int, error) {
	if err := s.preflightWrite(); err != nil {
		return 0, err
	}

	var value int

	// Atomic upsert per FR-2.7 — parameterized query, no string concatenation.
	err := s.writeDB.QueryRowContext(ctx,
		`INSERT INTO sequences (key, value) VALUES (?, 1)
		 ON CONFLICT(key) DO UPDATE SET value = value + 1
		 RETURNING value`,
		key,
	).Scan(&value)
	if err != nil {
		return 0, s.classifyWriteError(fmt.Errorf("next sequence for %s: %w", key, err))
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
