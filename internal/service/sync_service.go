// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// DefaultMaxImportSize is the default maximum file size for auto-import (50 MB).
const DefaultMaxImportSize = 50 * 1024 * 1024

// SyncService manages automatic import/export of .mtix/tasks.json per FR-15.
// It reads the export file exactly once into memory, computes its SHA-256 hash,
// and compares against a stored hash to detect changes from git operations.
type SyncService struct {
	store    *sqlite.Store
	logger   *slog.Logger
	clock    func() time.Time
	autoSync AutoSyncConfig // sync.auto_sync; nil reads as the default, on (MTIX-95.31.2)
	notices  io.Writer      // user-facing auto-import notices, stderr by default (MTIX-95.31.2)
	mu       sync.Mutex     // guards noticed
	noticed  map[string]bool
	// olderForms are the older conflict baseline forms hasConflict tries,
	// in order; nil means olderBaselineForms() (MTIX-95.31.11). Tests set it.
	olderForms []baselineForm
	// wrapBaselineFile wraps the temporary file a baseline rewrite writes
	// (replaceBaseline); nil writes the file directly (MTIX-95.31.11). Tests
	// set it to make the write or the close fail.
	wrapBaselineFile func(*os.File) io.WriteCloser

	MaxImportSize int64 // Maximum file size for auto-import per FR-15.2e.
}

// NewSyncService creates a SyncService per FR-15.2. Auto-import notices and
// refusals go to stderr (SetNoticeWriter changes that), and auto-import is
// on until SetAutoSyncConfig wires the sync.auto_sync switch.
func NewSyncService(store *sqlite.Store, logger *slog.Logger, clock func() time.Time) *SyncService {
	return &SyncService{
		store:         store,
		logger:        logger,
		clock:         clock,
		notices:       os.Stderr,
		MaxImportSize: DefaultMaxImportSize,
	}
}

// AutoImport reads .mtix/tasks.json, computes its hash, and imports it in
// replace mode if the hash differs from the stored hash per FR-15.2. The
// file is read exactly once to eliminate TOCTOU races between hash check
// and parse. MTIX-95.31.2: nothing is imported while sync.auto_sync is
// false, unless the store holds no nodes (FR-15.2a, FR-15.2j); the file is
// validated completely, and a replace that would delete local node or
// dependency data the file lacks is refused, changing nothing, printed once
// and returned as ErrAutoImportRefused (FR-15.2i); every other replace is
// preceded by a timestamped backup (FR-15.2f), followed by a one-line
// notice of what changed (FR-15.2k) and by a fresh conflict baseline.
// MTIX-95.31.4: a conflict (FR-15.2h) keeps and prints the loss list when
// the replace would also delete local data (refuseConflict), and the
// replace re-checks, in its own transaction, that the store is the one the
// loss check compared: if it changed, nothing is written and the error
// wraps sqlite.ErrStoreChangedSinceCheck, so the next command checks the
// file again.
func (s *SyncService) AutoImport(ctx context.Context, mtixDir string) error {
	if s.autoImportSwitchedOff(ctx) {
		return nil // FR-15.2j: auto-import is off; auto-export keeps running, except over a pulled board (FR-15.3e).
	}
	start := s.clock()

	// Acquire shared lock for import per FR-15.8.
	lockFile, lockErr := s.acquireLock(mtixDir, lockShared)
	if lockErr != nil {
		s.logger.Warn("could not acquire sync lock, skipping auto-import", "error", lockErr)
		return nil
	}
	defer s.releaseLock(lockFile)

	// Steps 1-4: read the file once, check its size, hash and compare.
	hashPath := filepath.Join(mtixDir, "data", "sync.sha256")
	data, fileHash, storedHash, skip, err := s.readAndHashTasksFile(filepath.Join(mtixDir, "tasks.json"), hashPath)
	if err != nil {
		return err
	}
	if skip || s.isOwnExport(ctx, hashPath, fileHash) {
		return nil
	}

	// Step 5: parse and validate the whole file; export the local store.
	incoming, local, conflict, err := s.parseAndValidateExport(ctx, data, mtixDir, fileHash)
	if err != nil || incoming == nil {
		return err
	}

	// Step 6: refuse a conflict, keeping what a replace would delete
	// (FR-15.2h, MTIX-95.31.4), and a replace that would delete local data
	// (FR-15.2i).
	diff, err := sqlite.DiffReplace(local, incoming)
	if err != nil {
		return fmt.Errorf("auto-import: compare tasks.json with the local store: %w", err)
	}
	if conflict {
		return s.refuseConflict(mtixDir, fileHash, diff)
	}
	if diff.Lossy() {
		return s.refuseLossyImport(mtixDir, fileHash, diff)
	}

	// Step 7: back up, then import in replace mode (FR-15.2d, FR-15.2f).
	backupPath, err := s.backupDB(ctx, mtixDir, fileHash, local)
	if err != nil {
		s.logger.Warn("backup before auto-import failed, skipping import", "error", err)
		s.recordRefusal(mtixDir, fileHash, refusalBackupFailed,
			"the local database could not be backed up before the import: "+err.Error())
		return nil
	}
	s.logger.Info("sync_import_triggered", "event", "sync_import_triggered", "file_hash", fileHash,
		"stored_hash", string(storedHash), "file_size", len(data), "node_count", incoming.NodeCount)
	// MTIX-95.31.4: nothing is written if the store changed after the loss check.
	unchanged := sqlite.IfStoreUnchanged(local.Checksum)
	if _, importErr := s.store.Import(ctx, incoming, sqlite.ImportModeReplace, false, unchanged); importErr != nil {
		return fmt.Errorf("auto-import: %w", importErr)
	}
	s.noticeImported(mtixDir, diff, backupPath) // FR-15.2k: never silent
	s.forgetBackup(mtixDir)                     // FR-15.2f: the next import backs up anew

	// Step 8: record the file as imported (FR-15.2h).
	return s.recordImported(ctx, mtixDir, hashPath, fileHash, incoming.NodeCount, start)
}

// recordImported runs after a successful auto-import: it updates the
// stored hash only then, and the conflict baseline, so the next pull is not
// a conflict (FR-15.2h), and logs the completion.
func (s *SyncService) recordImported(
	ctx context.Context, mtixDir, hashPath, fileHash string, nodeCount int, start time.Time,
) error {
	if hashErr := s.writeHashFile(hashPath, fileHash); hashErr != nil {
		return hashErr
	}
	s.refreshDBHash(ctx, mtixDir)
	s.logger.Info("sync_import_completed", "event", "sync_import_completed", "file_hash", fileHash,
		"node_count", nodeCount, "duration_ms", s.clock().Sub(start).Milliseconds())
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

// parseAndValidateExport parses tasks.json bytes, validates the schema
// version, exports the local store, checks for conflicts and runs every
// check import makes (sqlite.ValidateExport). It returns
// the file's export and the local store's, or nil exports (no error) when
// the import is skipped with a warning. A skip that needs the user's
// action is recorded for mtix sync (MTIX-95.31.2). A conflict (FR-15.2h)
// returns both exports with conflict true, unrecorded: the caller compares
// them first, to keep what a replace would delete (MTIX-95.31.4).
func (s *SyncService) parseAndValidateExport(
	ctx context.Context, data []byte, mtixDir, fileHash string,
) (incoming, local *sqlite.ExportData, conflict bool, err error) {
	incoming, err = s.decodeTasksFile(data, mtixDir, fileHash)
	if err != nil || incoming == nil {
		return nil, nil, false, err
	}

	local, err = s.store.Export(ctx, "", "")
	if err != nil {
		// Fail closed (MTIX-95.31.1): a store that cannot be exported may
		// hold changes tasks.json lacks, and a replace import would lose them.
		refused := fmt.Errorf("auto-import refused, nothing was imported: "+
			"the local store cannot be exported, so changes it holds that tasks.json lacks cannot be ruled out: "+
			"export the local store: %w", err)
		s.recordRefusal(mtixDir, fileHash, refusalUnreadableStore, refused.Error())
		return nil, nil, false, refused
	}

	conflict, err = s.hasConflict(mtixDir, local)
	if err != nil || conflict {
		return incoming, local, conflict, err
	}
	// The checks import makes, run before any backup; a file that fails
	// them is recorded, so no write overwrites it (MTIX-95.31.2).
	if err := sqlite.ValidateExport(incoming); err != nil {
		invalid := fmt.Errorf("auto-import: %w", err)
		s.recordRefusal(mtixDir, fileHash, refusalInvalidFile, invalid.Error())
		return nil, nil, false, invalid
	}
	return incoming, local, false, nil
}

// writeHashFile writes the hash to the given path, creating parent dirs.
// Uses filepath.Clean to sanitize the path per gosec G703.
func (s *SyncService) writeHashFile(hashPath, fileHash string) error {
	cleanPath := filepath.Clean(hashPath)
	if err := os.MkdirAll(filepath.Dir(cleanPath), 0755); err != nil {
		return fmt.Errorf("create sync data dir: %w", err)
	}
	// G703: every caller passes <project>/.mtix/data/sync.sha256, joined in
	// this package from the project's .mtix directory; no import path or
	// file content reaches the path.
	if err := os.WriteFile(cleanPath, []byte(fileHash), 0644); err != nil { //nolint:gosec // G703, see above
		return fmt.Errorf("write sync hash: %w", err)
	}
	return nil
}

// AutoExport writes the current DB state to .mtix/tasks.json per FR-15.3
// (exportBoard), unless that would overwrite a board that changed on disk
// and was not imported (MTIX-95.31.2, keepPulledBoard): it then runs the
// automatic import first, and when that does not import the board, keeps
// it, records the refusal as pending and says so in one line.
func (s *SyncService) AutoExport(ctx context.Context, mtixDir string) error {
	// MTIX-95.31.2: never overwrite a tasks.json that changed on disk and
	// was not imported; the change stays in the local store.
	if s.keepPulledBoard(ctx, mtixDir) {
		return nil
	}
	return s.exportBoard(ctx, mtixDir)
}

// exportBoard writes the current DB state to .mtix/tasks.json per FR-15.3,
// without the MTIX-95.31.2 check AutoExport makes first: deterministic
// export, atomic temp+rename write, then the file hash and the DB hash for
// conflict detection.
func (s *SyncService) exportBoard(ctx context.Context, mtixDir string) error {
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
	if err := writeFileAtomically(tasksPath, jsonBytes); err != nil {
		return err
	}

	// Step 4: Update file hash per FR-15.3d.
	fileHash := fmt.Sprintf("%x", sha256.Sum256(jsonBytes))
	if err := s.writeHashFile(hashPath, fileHash); err != nil {
		return err
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
	dbHash, hashErr := s.computeDBHash(ctx)
	if hashErr != nil {
		return fmt.Errorf("db hash after auto-export: %w", hashErr)
	}
	if err := os.WriteFile(dbHashPath, []byte(dbHash), 0644); err != nil {
		return fmt.Errorf("write db hash: %w", err)
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

// writeFileAtomically writes data to path through a temporary file in the
// same directory and a rename (FR-15.3c), so a crash never leaves a torn
// file.
func writeFileAtomically(path string, data []byte) error {
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, data, 0644); err != nil {
		return fmt.Errorf("write temp tasks.json: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp to tasks.json: %w", err)
	}
	return nil
}

// backupDB takes a verified snapshot of the local database before a
// replace-mode auto-import of the tasks.json with hash fileHash (FR-15.2f,
// amended by MTIX-95.31.2) and returns its path. The snapshot is written
// with VACUUM INTO and checked with PRAGMA quick_check (Store.Backup) to
// .mtix/data/backups/pre-sync-<UTC time, 20060102-150405>.db, with a -2,
// -3, ... suffix for further imports in the same second. A backup is
// reused only by a retry of an import that failed after taking it, while
// the local store (local, its export) is unchanged; once an import
// succeeds, the next one takes a new backup even of the same file. Only
// after a new backup succeeds are the oldest pre-import backups beyond
// PreSyncBackupsKept deleted; no other file in the directory is touched.
func (s *SyncService) backupDB(ctx context.Context, mtixDir, fileHash string, local *sqlite.ExportData) (string, error) {
	dir := filepath.Join(mtixDir, "data", "backups")
	storeHash, hashErr := exportHash(local, formCurrent)
	if hashErr != nil {
		storeHash = "" // never reuse a backup of a store state not known
	}
	if taken := s.backupTakenFor(mtixDir, fileHash, storeHash); taken != "" {
		return taken, nil
	}
	dest, err := s.writePreSyncBackup(ctx, dir) // new verified snapshot; prunes to PreSyncBackupsKept
	if err != nil {
		return "", err
	}
	s.rememberBackup(mtixDir, fileHash, storeHash, dest)
	s.logger.Debug("database backed up before auto-import", "backup", dest)
	return dest, nil
}

// hasConflict detects whether both the file and DB have changed since last
// sync per FR-15.2h. If both changed, the user must resolve manually. The
// caller passes the local store's export, taken before this check, so an
// unreadable store has already failed closed (MTIX-95.31.1). Without a
// stored DB hash there is no baseline and no conflict. MTIX-95.31.11: a
// baseline that differs only because it was hashed over an older form of
// the same, unchanged store (the 1.0.0 export mtix 0.5.3 wrote, the one
// without uids 0.3.0 wrote, or an export before the open-time backfill
// minted uids, matchOlderBaseline) is rewritten in the current form and is
// no conflict.
func (s *SyncService) hasConflict(mtixDir string, local *sqlite.ExportData) (bool, error) {
	currentDBHash, err := exportHash(local, formCurrent)
	if err != nil {
		return false, err
	}

	dbHashPath := filepath.Join(mtixDir, "data", "sync-db.sha256")
	storedDBHash, readErr := os.ReadFile(dbHashPath)
	if readErr != nil {
		// No stored DB hash → first run or DB hash tracking not set up.
		// No conflict possible without a baseline.
		return false, nil
	}
	if currentDBHash == string(storedDBHash) {
		return false, nil
	}
	form, older, err := matchOlderBaseline(local, string(storedDBHash), s.baselineForms())
	if err != nil {
		return false, fmt.Errorf("compare the conflict baseline with older forms: %w", err)
	}
	if older {
		s.upgradeBaseline(mtixDir, string(storedDBHash), currentDBHash, form)
		return false, nil
	}

	// DB hash differs AND file hash differs (we're in this code path because
	// file hash already differed) → conflict.
	return true, nil
}

// SyncReport describes the result of comparing SQLite state with tasks.json.
type SyncReport struct {
	InSync        bool     `json:"in_sync"`
	FileNodeCount int      `json:"file_node_count"`
	DBNodeCount   int      `json:"db_node_count"`
	OnlyInFile    []string `json:"only_in_file,omitempty"`
	OnlyInDB      []string `json:"only_in_db,omitempty"`
	// DifferentUID are the ids that tasks.json and the store both hold
	// under different uids (MTIX-95.31.4): the file's task under the id may
	// be another task.
	DifferentUID []string `json:"different_uid,omitempty"`
	// AutoImport is the automatic import state: whether sync.auto_sync
	// leaves it on, and the last auto-import mtix refused (MTIX-95.31.2).
	AutoImport AutoImportState `json:"auto_import"`
}

// Compare checks whether the SQLite database and .mtix/tasks.json are in sync.
// Returns a SyncReport describing any drift and the auto-import state
// (MTIX-95.31.2). Does not modify either store. MTIX-95.31.4: the two are
// never in sync while an auto-import of the file is pending, or while an id
// is held under different uids (DifferentUID).
func (s *SyncService) Compare(ctx context.Context, mtixDir string) (*SyncReport, error) {
	// Read tasks.json and extract node ids and uids via lightweight parsing.
	fileBytes, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	if err != nil {
		return nil, fmt.Errorf("read tasks.json for compare: %w", err)
	}
	fileUIDs, fileNodeCount, err := fileNodeUIDs(fileBytes)
	if err != nil {
		return nil, fmt.Errorf("parse tasks.json for compare: %w", err)
	}

	// Export current DB state.
	dbExport, err := s.store.Export(ctx, "", "")
	if err != nil {
		return nil, fmt.Errorf("export DB for compare: %w", err)
	}
	dbUIDs := exportNodeUIDs(dbExport)

	report := &SyncReport{
		FileNodeCount: fileNodeCount,
		DBNodeCount:   len(dbExport.Nodes),
		OnlyInFile:    idsMissingFrom(fileUIDs.ids(), dbUIDs.ids()),
		OnlyInDB:      idsMissingFrom(dbUIDs.ids(), fileUIDs.ids()),
		DifferentUID:  differentUIDs(fileUIDs, dbUIDs),
	}
	// MTIX-95.31.2: the auto-import switch and the last refusal.
	report.AutoImport = s.autoImportState(mtixDir, fileBytes)
	pending := report.AutoImport.LastRefusal != nil && report.AutoImport.LastRefusal.Pending
	report.InSync = len(report.OnlyInFile) == 0 && len(report.OnlyInDB) == 0 &&
		len(report.DifferentUID) == 0 && !pending
	return report, nil
}

// idsMissingFrom returns the ids in have that other lacks, sorted for
// deterministic output per DO-178C §5.1.2.
func idsMissingFrom(have, other map[string]bool) []string {
	var missing []string
	for id := range have {
		if !other[id] {
			missing = append(missing, id)
		}
	}
	sort.Strings(missing)
	return missing
}
