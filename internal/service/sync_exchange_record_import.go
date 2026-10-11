// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

// Apply retains the checked incoming snapshot until receipt publication.
package service

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

func (s *SyncService) applyReceivedImport(ctx context.Context, mtixDir, hashPath, fileHash string, data, storedHash []byte, incoming, local *sqlite.ExportData, diff *sqlite.ReplaceDiff, start time.Time) error {
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
	return s.recordImported(ctx, mtixDir, hashPath, fileHash, incoming, start)
}
