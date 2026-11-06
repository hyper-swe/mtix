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
	localOnly, knownErr := s.hubKnownChanges(ctx, ri)
	if knownErr != nil {
		return fmt.Errorf("auto-import: %w", knownErr)
	}
	importRes, importErr := s.store.Import(ctx, ri.written, sqlite.ImportModeReplace, false, opts...)
	if importErr != nil {
		return fmt.Errorf("auto-import: %w", importErr)
	}
	if recErr := s.store.RecordLocalOnlyImports(ctx, recordable(localOnly)); recErr != nil {
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

// hubKnownChanges returns, for a store that syncs, the tasks this import
// changes that the sync log carries, with the fields that change: those the
// diff lists as updated, and the tasks of added or removed dependencies. A
// store that does not sync has no hub to lose them to (MTIX-112.17.16.16).
func (s *SyncService) hubKnownChanges(ctx context.Context, ri *receivedImport) ([]sqlite.LocalOnlyImport, error) {
	synced := ri.assumeSynced
	if !synced {
		var err error
		if synced, err = s.store.HasSyncedWithHub(ctx); err != nil {
			return nil, fmt.Errorf("read whether the store syncs: %w", err)
		}
	}
	if !synced {
		return nil, nil
	}
	changes := sqlite.ReplaceFieldChanges(ri.local, ri.written, ri.diff)
	ids := make([]string, 0, len(changes))
	for _, c := range changes {
		ids = append(ids, c.ID)
	}
	known, err := s.store.HubKnownNodes(ctx, ids)
	if err != nil {
		return nil, err
	}
	keep := make(map[string]bool, len(known))
	for _, id := range known {
		keep[id] = true
	}
	var out []sqlite.LocalOnlyImport
	for _, c := range changes {
		if keep[c.ID] {
			out = append(out, c)
		}
	}
	return out, nil
}

// recordable keeps, of each change, the fields a sync event carries, and
// leaves out a task none of whose fields do: nothing replaces or sends it.
func recordable(changes []sqlite.LocalOnlyImport) []sqlite.LocalOnlyImport {
	var out []sqlite.LocalOnlyImport
	for _, c := range changes {
		if f := sqlite.SettleableFields(c.Fields); len(f) > 0 {
			out = append(out, sqlite.LocalOnlyImport{ID: c.ID, Fields: f})
		}
	}
	return out
}
