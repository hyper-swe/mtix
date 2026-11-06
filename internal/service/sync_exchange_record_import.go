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

// receivedImport is one automatic import ready to apply: the file read, the
// board the import writes (written: the file itself, or the file resolved
// against the base, MTIX-95.31.15.1) and what the loss check found.
type receivedImport struct {
	mtixDir, hashPath, fileHash string
	data, storedHash            []byte
	incoming, local, written    *sqlite.ExportData
	diff                        *sqlite.ReplaceDiff
	resolution                  *sqlite.BaseResolution
	start                       time.Time
	// assumeSynced: transient hub evidence (an exported DSN) makes the replace
	// guards treat the store as synced (sqlite.AsSyncedStore, MTIX-107.31.8).
	assumeSynced bool
}

func (s *SyncService) applyReceivedImport(ctx context.Context, ri *receivedImport) error {
	// Step 7: back up, then import in replace mode (FR-15.2d, FR-15.2f).
	backupPath, err := s.backupDB(ctx, ri.mtixDir, ri.fileHash, ri.local)
	if err != nil {
		s.logger.Warn("backup before auto-import failed, skipping import", "error", err)
		s.recordRefusal(ri.mtixDir, ri.fileHash, refusalBackupFailed,
			"the local database could not be backed up before the import: "+err.Error())
		return nil
	}
	s.logger.Info("sync_import_triggered", "event", "sync_import_triggered", "file_hash", ri.fileHash,
		"stored_hash", string(ri.storedHash), "file_size", len(ri.data), "node_count", ri.incoming.NodeCount)
	// MTIX-95.31.4: nothing is written if the store changed after the loss check.
	opts := []sqlite.ImportOption{sqlite.IfStoreUnchanged(ri.local.Checksum)}
	if ri.assumeSynced {
		opts = append(opts, sqlite.AsSyncedStore())
	}
	// MTIX-112.17.16.16: the tasks the hub knows that this import changes; the
	// import journals nothing, so their new values stay local until a push.
	localOnly, knownErr := s.store.HubKnownNodes(ctx, ri.diff.Updated)
	if knownErr != nil {
		return fmt.Errorf("auto-import: %w", knownErr)
	}
	importRes, importErr := s.store.Import(ctx, ri.written, sqlite.ImportModeReplace, false, opts...)
	if importErr != nil {
		return fmt.Errorf("auto-import: %w", importErr)
	}
	if recErr := s.store.RecordLocalOnlyImports(ctx, localOnly); recErr != nil {
		s.logger.Warn("record local-only imports failed", "error", recErr)
	}
	s.noticeImported(ri.mtixDir, ri.diff, backupPath, localOnly) // FR-15.2k: never silent
	s.noticeResolved(ri.resolution)
	s.noticeHistoryImport(importRes)
	s.noticeOlderWriter(ri.incoming.MtixVersion) // MTIX-95.31.19.9: once per imported file
	s.forgetBackup(ri.mtixDir)                   // FR-15.2f: the next import backs up anew

	// Step 8: record the file as imported (FR-15.2h).
	return s.recordImported(ctx, ri.mtixDir, ri.hashPath, ri.fileHash, ri.incoming, ri.start)
}
