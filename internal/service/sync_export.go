// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// AutoExport publishes changed DB content to .mtix/tasks.json per FR-15.3,
// retaining the existing bytes and timestamp when content is unchanged,
// (exportBoard), unless that would overwrite a board that changed on disk
// and was not imported (MTIX-95.31.2, keepPulledBoard): it then runs the
// automatic import first, and when that does not import the board, keeps
// it, records the refusal as pending and says so in one line.
func (s *SyncService) AutoExport(ctx context.Context, mtixDir string) error {
	markErr := s.RequestExport(mtixDir)
	return errors.Join(markErr, s.runAutoExport(ctx, mtixDir, automaticExportPasses))
}

const automaticExportPasses = 3

// runAutoExport bounds catch-up work and releases each lock before the next
// pass. Contention, refusal, metadata errors and publication errors stop it.
func (s *SyncService) runAutoExport(ctx context.Context, mtixDir string, passes int) error {
	for pass := 0; pass < passes; pass++ {
		if blocked, err := s.keepPulledBoard(ctx, mtixDir); blocked || err != nil {
			return err
		}
		completed, err := s.exportBoardPass(ctx, mtixDir, true)
		if err != nil || !completed {
			return err
		}
		status, err := s.PendingExports(ctx, mtixDir)
		if err != nil || status.Count == 0 {
			return err
		}
	}
	return nil
}

// exportBoard writes the current DB state to .mtix/tasks.json per FR-15.3,
// without the MTIX-95.31.2 check AutoExport makes first: deterministic
// export, atomic temp+rename write, then the file hash and the DB hash for
// conflict detection.
func (s *SyncService) exportBoard(ctx context.Context, mtixDir string) error {
	return s.exportBoardProtected(ctx, mtixDir, false)
}

func (s *SyncService) exportBoardProtected(ctx context.Context, mtixDir string, protect bool) error {
	_, err := s.exportBoardPass(ctx, mtixDir, protect)
	return err
}

func (s *SyncService) exportBoardPass(ctx context.Context, mtixDir string, protect bool) (bool, error) {
	start := s.clock()
	lockFile, lockErr := s.acquireLock(mtixDir, lockExclusive)
	if lockErr != nil {
		s.logger.Warn("could not acquire sync lock, skipping auto-export", "error", lockErr)
		return false, nil
	}
	defer s.releaseLock(lockFile)
	if protect {
		if blocked, err := s.keepPulledBoardUnderLock(mtixDir); blocked || err != nil {
			return false, err
		}
	}
	cutoff, snapshotErr := s.snapshotPendingExport(ctx, mtixDir)
	if err := s.publishExport(ctx, mtixDir, start, protect); err != nil {
		return false, errors.Join(snapshotErr, err)
	}
	// An unreadable request snapshot cannot authorize deleting any requests.
	if snapshotErr != nil {
		return true, snapshotErr
	}
	return true, s.retireExportSnapshot(ctx, cutoff)
}

func (s *SyncService) retireExportSnapshot(ctx context.Context, cutoff int64) error {
	// Successful publication must finish its durability bookkeeping even when
	// a shutdown observer cancels the operation at the completed-publication log.
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if err := s.store.RetireExportRequest(cleanup, cutoff); err != nil {
		return fmt.Errorf("retire published export cutoff: %w", err)
	}
	return nil
}

func (s *SyncService) snapshotPendingExport(ctx context.Context, mtixDir string) (int64, error) {
	if err := s.migratePendingExports(ctx, mtixDir); err != nil {
		return 0, err
	}
	// This FULL-WAL metadata commit must finish before Store.Export begins.
	return s.store.SnapshotExportRequest(ctx)
}

func (s *SyncService) publishExport(ctx context.Context, mtixDir string, start time.Time, automatic bool) error {
	tasksPath := filepath.Join(mtixDir, "tasks.json")

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

	// FR-15.3 (MTIX-150): elapsed time alone is not a board change. Keep
	// exact published bytes while completing the same durability bookkeeping.
	if automatic {
		published, err := matchingPublishedExport(tasksPath, exportData)
		if err != nil {
			return err
		}
		if published != nil {
			return s.recordUnchangedExport(ctx, mtixDir, published, exportData)
		}
	}

	// Step 3: Atomic write via temp file + rename per FR-15.3c.
	if err := writeFileAtomically(tasksPath, jsonBytes); err != nil {
		return err
	}

	// Step 4: Update file hash per FR-15.3d.
	fileHash := fmt.Sprintf("%x", sha256.Sum256(jsonBytes))
	if err := s.publishExportHashes(ctx, mtixDir, fileHash, exportData); err != nil {
		return err
	}

	if err := syncExportPublicationWithSync(mtixDir, s.syncDirectory); err != nil {
		return err
	}
	elapsed := time.Since(start)
	s.logger.Debug("sync_export_completed",
		"event", "sync_export_completed",
		"file_hash", fileHash,
		"node_count", exportData.NodeCount,
		"file_size", len(jsonBytes),
		"duration_ms", elapsed.Milliseconds())

	return nil
}

// publishExportHashes records the baseline from the exact published snapshot,
// never a second export that could include a later writer commit.
func (s *SyncService) publishExportHashes(ctx context.Context, mtixDir, fileHash string, data *sqlite.ExportData) error {
	hashPath := filepath.Join(mtixDir, "data", "sync.sha256")
	dbHashPath := filepath.Join(mtixDir, "data", "sync-db.sha256")
	if err := s.writeHashFile(hashPath, fileHash); err != nil {
		return err
	}

	// Step 4b: Store export hash in meta table for redundant import detection.
	// Sibling agents sharing this DB will see this hash and skip import.
	if _, metaErr := s.store.WriteDB().ExecContext(ctx,
		"INSERT OR REPLACE INTO meta (key, value) VALUES ('last_export_hash', ?)",
		fileHash,
	); metaErr != nil {
		return fmt.Errorf("write last_export_hash to meta: %w", metaErr)
	}

	// Step 5: Update DB hash for conflict detection per FR-15.2h.
	dbHash, hashErr := exportHash(data, formCurrent)
	if hashErr != nil {
		return fmt.Errorf("db hash after auto-export: %w", hashErr)
	}
	// #nosec G703 -- dbHashPath is fixed data/sync-db.sha256 under the caller-selected local project; exported content supplies only hash bytes.
	if err := os.WriteFile(dbHashPath, []byte(dbHash), 0644); err != nil {
		return fmt.Errorf("write db hash: %w", err)
	}

	return nil
}
