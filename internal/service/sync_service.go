// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// DefaultMaxImportSize is the default maximum file size for auto-import (50 MB).
const DefaultMaxImportSize = 50 * 1024 * 1024

// supportedSchemaVersion is the maximum major version this build supports.
const supportedSchemaVersion = "1.0.0"

// SyncService manages automatic import/export of .mtix/tasks.json per FR-15.
// It reads the export file exactly once into memory, computes its SHA-256 hash,
// and compares against a stored hash to detect changes from git operations.
type SyncService struct {
	store          *sqlite.Store
	logger         *slog.Logger
	clock          func() time.Time
	MaxImportSize  int64 // Maximum file size for auto-import per FR-15.2e.
}

// NewSyncService creates a SyncService per FR-15.2.
func NewSyncService(store *sqlite.Store, logger *slog.Logger, clock func() time.Time) *SyncService {
	return &SyncService{
		store:         store,
		logger:        logger,
		clock:         clock,
		MaxImportSize: DefaultMaxImportSize,
	}
}

// AutoImport reads .mtix/tasks.json, computes its hash, and imports if the
// hash differs from the stored hash per FR-15.2. The file is read exactly
// once to eliminate TOCTOU races between hash check and parse.
func (s *SyncService) AutoImport(ctx context.Context, mtixDir string) error {
	start := s.clock()

	// Acquire shared lock for import per FR-15.8.
	lockFile, lockErr := s.acquireLock(mtixDir, lockShared)
	if lockErr != nil {
		s.logger.Warn("could not acquire sync lock, skipping auto-import", "error", lockErr)
		return nil
	}
	defer s.releaseLock(lockFile)

	tasksPath := filepath.Join(mtixDir, "tasks.json")
	hashPath := filepath.Join(mtixDir, "data", "sync.sha256")

	// Step 1-4: Read file, validate size, compute hash, compare with stored.
	data, fileHash, storedHash, skip, err := s.readAndHashTasksFile(tasksPath, hashPath)
	if err != nil {
		return err
	}
	if skip {
		return nil
	}

	// Step 4b: Check if file was produced by our own export.
	// If the file hash matches last_export_hash in the meta table, the file
	// came from this database (our export or a sibling agent's export).
	// Skip import to avoid redundant ImportModeReplace and the thundering
	// herd problem with multiple agents on the same machine.
	var lastExportHash string
	queryErr := s.store.QueryRow(ctx,
		"SELECT value FROM meta WHERE key = 'last_export_hash'",
	).Scan(&lastExportHash)
	if queryErr == nil && lastExportHash == fileHash {
		// File is from our own export — update sync.sha256 to prevent
		// re-checking on next command, then skip.
		if hashErr := s.writeHashFile(hashPath, fileHash); hashErr != nil {
			s.logger.Warn("failed to update sync hash after export-match skip", "error", hashErr)
		}
		return nil
	}
	if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
		// Genuine DB error (not "key not found") — log for audit trail
		// per NASA-STD-8739.8 §7.2, then proceed with import as fail-safe.
		s.logger.Warn("failed to read last_export_hash from meta, proceeding with import", "error", queryErr)
	}

	// Step 5: Parse and validate the already-read bytes.
	exportData, err := s.parseAndValidateExport(ctx, data, tasksPath, mtixDir)
	if err != nil {
		return err
	}
	if exportData == nil {
		// Validation returned a warning (format/schema/conflict) — skip silently.
		return nil
	}

	// Step 6: Import in replace mode (FR-15.2, FR-15.2d atomic transaction).
	s.logger.Info("sync_import_triggered",
		"event", "sync_import_triggered",
		"file_hash", fileHash,
		"stored_hash", string(storedHash),
		"file_size", len(data),
		"node_count", exportData.NodeCount)

	if _, importErr := s.store.Import(ctx, exportData, sqlite.ImportModeReplace, false); importErr != nil {
		return fmt.Errorf("auto-import: %w", importErr)
	}

	// Step 7: Update stored hash only after successful import (FR-15.2).
	if err := s.writeHashFile(hashPath, fileHash); err != nil {
		return err
	}

	elapsed := time.Since(start)
	s.logger.Info("sync_import_completed",
		"event", "sync_import_completed",
		"file_hash", fileHash,
		"node_count", exportData.NodeCount,
		"duration_ms", elapsed.Milliseconds())

	return nil
}

// readAndHashTasksFile reads the tasks file, validates its size, computes its
// SHA-256 hash, and compares with the stored hash. Returns skip=true when no
// import is needed (file missing or hash unchanged).
func (s *SyncService) readAndHashTasksFile(
	tasksPath, hashPath string,
) (data []byte, fileHash string, storedHash []byte, skip bool, err error) {
	data, err = os.ReadFile(tasksPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			s.logger.Debug("tasks.json not found, skipping auto-import",
				"path", tasksPath)
			return nil, "", nil, true, nil
		}
		return nil, "", nil, false, fmt.Errorf("read tasks.json: %w", err)
	}

	if int64(len(data)) > s.MaxImportSize {
		return nil, "", nil, false, fmt.Errorf(
			"tasks.json (%d bytes) exceeds maximum import size (%d bytes): %w",
			len(data), s.MaxImportSize, model.ErrInvalidInput)
	}

	hash := sha256.Sum256(data)
	fileHash = fmt.Sprintf("%x", hash)

	storedHash, readErr := os.ReadFile(hashPath)
	if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
		return nil, "", nil, false, fmt.Errorf("read stored hash: %w", readErr)
	}

	if string(storedHash) == fileHash {
		s.logger.Debug("tasks.json hash unchanged, skipping auto-import",
			"hash", fileHash)
		return nil, "", nil, true, nil
	}

	return data, fileHash, storedHash, false, nil
}

// parseAndValidateExport parses tasks.json bytes, validates schema version,
// checks for conflicts, and backs up the DB. Returns nil ExportData (no error)
// when import should be skipped due to warnings.
func (s *SyncService) parseAndValidateExport(
	ctx context.Context, data []byte, tasksPath, mtixDir string,
) (*sqlite.ExportData, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] != '{' {
		s.logger.Warn("tasks.json is not in mtix ExportData format (expected JSON object, got array or other type)",
			"path", tasksPath,
			"fix", "run 'mtix export' to regenerate tasks.json in the correct format, or 'mtix sync --fix' to re-export from the database")
		return nil, nil
	}

	var exportData sqlite.ExportData
	if err := json.Unmarshal(data, &exportData); err != nil {
		return nil, fmt.Errorf("parse tasks.json: %w", err)
	}

	schemaVer := exportData.SchemaVersion
	if schemaVer == "" {
		schemaVer = "1.0.0"
	}
	if !isSchemaCompatible(schemaVer) {
		s.logger.Error("tasks.json schema version is newer than supported — upgrade mtix",
			"file_version", schemaVer,
			"supported_version", supportedSchemaVersion)
		return nil, nil
	}

	if s.hasConflict(ctx, mtixDir) {
		s.logger.Warn("conflict detected: both tasks.json and local database changed since last sync",
			"resolution", "run 'mtix import --mode replace' or 'mtix export' to resolve")
		return nil, nil
	}

	if err := s.backupDB(mtixDir); err != nil {
		s.logger.Warn("backup before auto-import failed, skipping import", "error", err)
		return nil, nil
	}

	return &exportData, nil
}

// writeHashFile writes the hash to the given path, creating parent dirs.
// Uses filepath.Clean to sanitize the path per gosec G703.
func (s *SyncService) writeHashFile(hashPath, fileHash string) error {
	cleanPath := filepath.Clean(hashPath)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0755); err != nil {
		return fmt.Errorf("create sync data dir: %w", err)
	}
	if err := os.WriteFile(cleanPath, []byte(fileHash), 0644); err != nil {
		return fmt.Errorf("write sync hash: %w", err)
	}
	return nil
}

// AutoExport writes the current DB state to .mtix/tasks.json per FR-15.3.
// It exports deterministically, writes atomically via temp+rename,
// and updates both file hash and DB hash for conflict detection.
func (s *SyncService) AutoExport(ctx context.Context, mtixDir string) error {
	start := s.clock()

	// Acquire exclusive lock for export per FR-15.8.
	lockFile, lockErr := s.acquireLock(mtixDir, lockExclusive)
	if lockErr != nil {
		s.logger.Warn("could not acquire sync lock, skipping auto-export", "error", lockErr)
		return nil
	}
	defer s.releaseLock(lockFile)

	tasksPath := filepath.Join(mtixDir, "tasks.json")
	hashPath := filepath.Join(mtixDir, "data", "sync.sha256")
	dbHashPath := filepath.Join(mtixDir, "data", "sync-db.sha256")

	// Step 1: Export current DB state.
	exportData, err := s.store.Export(ctx, "", "")
	if err != nil {
		return fmt.Errorf("export for auto-export: %w", err)
	}

	// Step 2: Marshal to indented JSON for readability and determinism.
	jsonBytes, err := json.MarshalIndent(exportData, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal export data: %w", err)
	}

	// Step 3: Atomic write via temp file + rename per FR-15.3c.
	tmpPath := tasksPath + ".tmp"
	if err := os.WriteFile(tmpPath, jsonBytes, 0644); err != nil {
		return fmt.Errorf("write temp tasks.json: %w", err)
	}
	if err := os.Rename(tmpPath, tasksPath); err != nil {
		return fmt.Errorf("rename temp to tasks.json: %w", err)
	}

	// Step 4: Update file hash per FR-15.3d.
	fileHash := fmt.Sprintf("%x", sha256.Sum256(jsonBytes))
	if err := os.MkdirAll(filepath.Dir(hashPath), 0755); err != nil {
		return fmt.Errorf("create sync data dir: %w", err)
	}
	if err := os.WriteFile(hashPath, []byte(fileHash), 0644); err != nil {
		return fmt.Errorf("write file hash: %w", err)
	}

	// Step 4b: Store export hash in meta table for redundant import detection.
	// Sibling agents sharing this DB will see this hash and skip import.
	if _, metaErr := s.store.WriteDB().ExecContext(ctx,
		"INSERT OR REPLACE INTO meta (key, value) VALUES ('last_export_hash', ?)",
		fileHash,
	); metaErr != nil {
		s.logger.Warn("failed to write last_export_hash to meta", "error", metaErr)
	}

	// Step 5: Update DB hash for conflict detection per FR-15.2h.
	dbHash := s.computeDBHash(ctx)
	if dbHash != "" {
		if err := os.WriteFile(dbHashPath, []byte(dbHash), 0644); err != nil {
			return fmt.Errorf("write db hash: %w", err)
		}
	}

	elapsed := time.Since(start)
	s.logger.Info("sync_export_completed",
		"event", "sync_export_completed",
		"file_hash", fileHash,
		"node_count", exportData.NodeCount,
		"file_size", len(jsonBytes),
		"duration_ms", elapsed.Milliseconds())

	return nil
}

// backupDB copies the database file to pre-sync-backup.db per FR-15.2f.
// If the database file doesn't exist (first import), this is a no-op.
func (s *SyncService) backupDB(mtixDir string) error {
	dbPath := filepath.Join(mtixDir, "data", "mtix.db")
	backupPath := filepath.Join(mtixDir, "data", "pre-sync-backup.db")

	src, err := os.Open(dbPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// No database yet (first import) — nothing to back up.
			return nil
		}
		return fmt.Errorf("open database for backup: %w", err)
	}
	defer func() {
		if closeErr := src.Close(); closeErr != nil {
			s.logger.Error("failed to close source db during backup", "error", closeErr)
		}
	}()

	dst, err := os.Create(backupPath)
	if err != nil {
		return fmt.Errorf("create backup file: %w", err)
	}
	defer func() {
		if closeErr := dst.Close(); closeErr != nil {
			s.logger.Error("failed to close backup file", "error", closeErr)
		}
	}()

	if _, err := io.Copy(dst, src); err != nil {
		return fmt.Errorf("copy database to backup: %w", err)
	}

	s.logger.Debug("database backed up before auto-import", "backup", backupPath)
	return nil
}

// hasConflict detects whether both the file and DB have changed since last
// sync per FR-15.2h. If both changed, the user must resolve manually.
func (s *SyncService) hasConflict(ctx context.Context, mtixDir string) bool {
	dbHashPath := filepath.Join(mtixDir, "data", "sync-db.sha256")
	storedDBHash, err := os.ReadFile(dbHashPath)
	if err != nil {
		// No stored DB hash → first run or DB hash tracking not set up.
		// No conflict possible without a baseline.
		return false
	}

	// Compute current DB hash by exporting and hashing.
	currentDBHash := s.computeDBHash(ctx)
	if currentDBHash == "" {
		return false
	}

	// If DB hash matches stored, DB hasn't changed → no conflict.
	if currentDBHash == string(storedDBHash) {
		return false
	}

	// DB hash differs AND file hash differs (we're in this code path because
	// file hash already differed) → conflict.
	return true
}

// computeDBHash exports the current DB state and computes its SHA-256 hash.
// computeDBHash exports the current DB state and computes its SHA-256 hash.
// The ExportedAt timestamp is zeroed before hashing so that the hash reflects
// only data content, not when the export was generated. Without this, two
// exports of identical data in different seconds produce different hashes,
// causing false-positive conflict detection in hasConflict.
func (s *SyncService) computeDBHash(ctx context.Context) string {
	data, err := s.store.Export(ctx, "", "")
	if err != nil {
		s.logger.Debug("failed to export DB for conflict detection", "error", err)
		return ""
	}
	// Zero envelope metadata that changes between calls but doesn't
	// represent actual data changes.
	data.ExportedAt = ""
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		return ""
	}
	hash := sha256.Sum256(jsonBytes)
	return fmt.Sprintf("%x", hash)
}

// SyncReport describes the result of comparing SQLite state with tasks.json.
type SyncReport struct {
	InSync        bool     `json:"in_sync"`
	FileNodeCount int      `json:"file_node_count"`
	DBNodeCount   int      `json:"db_node_count"`
	OnlyInFile    []string `json:"only_in_file,omitempty"`
	OnlyInDB      []string `json:"only_in_db,omitempty"`
}

// Compare checks whether the SQLite database and .mtix/tasks.json are in sync.
// Returns a SyncReport describing any drift. Does not modify either store.
func (s *SyncService) Compare(ctx context.Context, mtixDir string) (*SyncReport, error) {
	tasksPath := filepath.Join(mtixDir, "tasks.json")

	// Read tasks.json and extract node IDs via lightweight JSON parsing.
	// We use a partial struct to avoid depending on unexported exportNode type.
	fileBytes, err := os.ReadFile(tasksPath)
	if err != nil {
		return nil, fmt.Errorf("read tasks.json for compare: %w", err)
	}

	var fileData struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	if unmarshalErr := json.Unmarshal(fileBytes, &fileData); unmarshalErr != nil {
		return nil, fmt.Errorf("parse tasks.json for compare: %w", unmarshalErr)
	}

	// Export current DB state.
	dbExport, err := s.store.Export(ctx, "", "")
	if err != nil {
		return nil, fmt.Errorf("export DB for compare: %w", err)
	}

	// Re-marshal and re-parse to extract IDs uniformly (dbExport.Nodes is
	// unexported exportNode type; marshal→unmarshal gives us access to IDs).
	dbBytes, err := json.Marshal(dbExport)
	if err != nil {
		return nil, fmt.Errorf("marshal DB export for compare: %w", err)
	}
	var dbData struct {
		Nodes []struct {
			ID string `json:"id"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(dbBytes, &dbData); err != nil {
		return nil, fmt.Errorf("parse DB export for compare: %w", err)
	}

	// Build ID sets.
	fileIDs := make(map[string]bool, len(fileData.Nodes))
	for _, n := range fileData.Nodes {
		if n.ID != "" {
			fileIDs[n.ID] = true
		}
	}

	dbIDs := make(map[string]bool, len(dbData.Nodes))
	for _, n := range dbData.Nodes {
		if n.ID != "" {
			dbIDs[n.ID] = true
		}
	}

	report := &SyncReport{
		FileNodeCount: len(fileData.Nodes),
		DBNodeCount:   len(dbData.Nodes),
	}

	for id := range fileIDs {
		if !dbIDs[id] {
			report.OnlyInFile = append(report.OnlyInFile, id)
		}
	}
	for id := range dbIDs {
		if !fileIDs[id] {
			report.OnlyInDB = append(report.OnlyInDB, id)
		}
	}

	// Sort for deterministic output per DO-178C §5.1.2.
	sort.Strings(report.OnlyInFile)
	sort.Strings(report.OnlyInDB)

	report.InSync = len(report.OnlyInFile) == 0 && len(report.OnlyInDB) == 0
	return report, nil
}

// isSchemaCompatible checks if the file's schema version is compatible
// with this build per FR-15.2g. Compatible if major versions match.
func isSchemaCompatible(fileVersion string) bool {
	fileMajor := parseMajorVersion(fileVersion)
	supportedMajor := parseMajorVersion(supportedSchemaVersion)
	return fileMajor <= supportedMajor
}

// parseMajorVersion extracts the major version number from a semver string.
// Returns 1 for empty or unparsable versions (backward compatibility default).
func parseMajorVersion(version string) int {
	if version == "" {
		return 1
	}
	parts := strings.SplitN(version, ".", 2)
	major, err := strconv.Atoi(parts[0])
	if err != nil {
		return 1
	}
	return major
}
