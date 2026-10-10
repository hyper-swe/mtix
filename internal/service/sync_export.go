// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// AutoExport writes the current DB state to .mtix/tasks.json per FR-15.3
// (exportBoard), unless that would overwrite a board that changed on disk
// and was not imported (MTIX-95.31.2, keepPulledBoard): it then runs the
// automatic import first, and when that does not import the board, keeps
// it, records the refusal as pending and says so in one line.
func (s *SyncService) AutoExport(ctx context.Context, mtixDir string) error {
	if err := markPendingExport(mtixDir); err != nil {
		return err
	}
	return s.runAutoExport(ctx, mtixDir, true)
}

func (s *SyncService) runAutoExport(ctx context.Context, mtixDir string, followup bool) error {
	// MTIX-95.31.2: never overwrite a tasks.json that changed on disk and
	// was not imported; the change stays in the local store.
	if blocked, err := s.keepPulledBoard(ctx, mtixDir); blocked || err != nil {
		return err
	}
	if err := s.exportBoardProtected(ctx, mtixDir, true); err != nil {
		return err
	}
	if followup {
		return s.DrainPendingExport(ctx, mtixDir)
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
	start := s.clock()

	// Acquire exclusive lock for export per FR-15.8.
	lockFile, lockErr := s.acquireLock(mtixDir, lockExclusive)
	if lockErr != nil {
		s.logger.Warn("could not acquire sync lock, skipping auto-export", "error", lockErr)
		return nil
	}
	defer s.releaseLock(lockFile)

	if protect {
		if blocked, err := s.keepPulledBoardUnderLock(mtixDir); blocked || err != nil {
			return err
		}
	}
	requests, err := pendingExportRequests(mtixDir)
	if err != nil {
		return err
	}
	if err := s.publishExport(ctx, mtixDir, start); err != nil {
		return err
	}
	return retirePendingExports(mtixDir, requests)
}

func (s *SyncService) publishExport(ctx context.Context, mtixDir string, start time.Time) error {
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

	// Step 3: Atomic write via temp file + rename per FR-15.3c.
	if err := writeFileAtomically(tasksPath, jsonBytes); err != nil {
		return err
	}

	// Step 4: Update file hash per FR-15.3d.
	fileHash := fmt.Sprintf("%x", sha256.Sum256(jsonBytes))
	if err := s.publishExportHashes(ctx, mtixDir, fileHash); err != nil {
		return err
	}

	if err := syncExportPublication(mtixDir); err != nil {
		return err
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

// publishExportHashes keeps the existing post-export DB baseline computation.
func (s *SyncService) publishExportHashes(ctx context.Context, mtixDir, fileHash string) error {
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
	dbHash, hashErr := s.computeDBHash(ctx)
	if hashErr != nil {
		return fmt.Errorf("db hash after auto-export: %w", hashErr)
	}
	if err := os.WriteFile(dbHashPath, []byte(dbHash), 0644); err != nil {
		return fmt.Errorf("write db hash: %w", err)
	}

	return nil
}
