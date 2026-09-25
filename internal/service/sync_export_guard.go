// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The kinds of AutoImportRefusal (MTIX-95.31.2).
const (
	refusalLossy           = "lossy"
	refusalConflict        = "conflict"
	refusalNewerSchema     = "newer_schema"
	refusalInvalidFile     = "invalid_file"
	refusalUnreadableStore = "unreadable_store"
	refusalBackupFailed    = "backup_failed"
	refusalNotImported     = "not_imported"
)

// resolutionFor names the way out of a pending refusal, by its kind.
// A newer schema needs an upgrade: mtix sync --fix would rewrite the board
// in this build's older format. When the refusal records what a replace
// of the file would delete, the replace option names it (MTIX-95.31.4).
// A conflict's line says what the merge keeps and names the way out when
// nothing changed locally, and a not_imported line says what the merge
// keeps (MTIX-95.31.11).
func resolutionFor(refusal *AutoImportRefusal) string {
	replace := "mtix import .mtix/tasks.json --mode replace"
	if refusal.Loss != "" {
		replace += ", which deletes " + refusal.Loss
	}
	switch refusal.Kind {
	case refusalNewerSchema:
		return "It was written by a newer mtix: upgrade mtix, then run any mtix command. " +
			"Rewriting it from the local store would downgrade it to this older format"
	case refusalConflict:
		return "Both it and the local store changed since the last sync: merge them with " +
			"mtix import .mtix/tasks.json --mode merge (" + mergeKeeps + "), keep the local store with " +
			"mtix sync --fix, or keep the file with " + replace + "; " + unchangedRecovery
	case refusalInvalidFile:
		return "It fails its checks (mtix sync shows why): if it was edited or merged by hand, repair and check it, " +
			"then run mtix import .mtix/tasks.json --recompute-checksum; to keep the local board, run mtix sync --fix"
	case refusalUnreadableStore:
		return "The local store cannot be read: run mtix recover (see its report), then import the file"
	case refusalBackupFailed:
		return "The backup before the import could not be written: free disk space; the next command retries"
	case refusalNotImported:
		return "Import it with mtix import .mtix/tasks.json --mode merge (" + mergeKeeps + "), " +
			"or keep the local board with mtix sync --fix"
	}
	return "Resolve it with mtix import .mtix/tasks.json --mode merge, mtix sync --fix or " + replace
}

// keepPulledBoard reports whether auto-export must leave .mtix/tasks.json
// as it is (MTIX-95.31.2): an auto-import of it is pending, or it changed on
// disk since mtix last wrote or imported it and the automatic import run
// now does not import it. Then the refusal is recorded as pending and one
// line says so. This protects a pulled board from every writer, the CLI and
// the long-running ones (MCP server, mtix serve, daemon) alike, which
// auto-import only when they start.
func (s *SyncService) keepPulledBoard(ctx context.Context, mtixDir string) bool {
	if s.exportBlockedByPendingImport(mtixDir) {
		return true
	}
	diskHash, changed := s.boardChangedOnDisk(mtixDir)
	if !changed {
		return false
	}
	importErr := s.AutoImport(ctx, mtixDir)
	if _, stillChanged := s.boardChangedOnDisk(mtixDir); !stillChanged {
		return false // imported: the export writes the store, the board included
	}
	if refusal := s.readRefusal(mtixDir); refusal == nil || refusal.FileHash != diskHash || refusal.ResolvedAt != "" {
		reason := "tasks.json changed on disk and was not imported"
		switch {
		case importErr != nil:
			reason += ": " + importErr.Error()
		case !s.AutoImportEnabled():
			reason += ": automatic import is off (sync.auto_sync is false)"
		}
		s.recordRefusal(mtixDir, diskHash, refusalNotImported, reason)
	}
	return s.exportBlockedByPendingImport(mtixDir)
}

// boardChangedOnDisk returns the hash of .mtix/tasks.json and whether it
// changed since mtix last wrote or imported it: its hash is not the stored
// one (sync.sha256, written by every export and import). A file this
// database exported but whose stored hash is stale (a sibling process
// between its write and its hash update) counts as changed; the automatic
// import run next recognizes it (isOwnExport) and records its hash. A
// missing or unreadable file has nothing to protect.
func (s *SyncService) boardChangedOnDisk(mtixDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	if err != nil {
		return "", false
	}
	diskHash := fmt.Sprintf("%x", sha256.Sum256(data))
	stored, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	return diskHash, err != nil || string(stored) != diskHash
}

// exportBlockedByPendingImport reports whether auto-export must leave
// .mtix/tasks.json alone because an auto-import of it is pending: the file
// on disk was refused and not resolved (MTIX-95.31.2). Rewriting it from
// the local store would silently drop every change it carries. The first
// time per pending file it prints one line saying so and how to resolve
// that kind of refusal.
func (s *SyncService) exportBlockedByPendingImport(mtixDir string) bool {
	refusal := s.readRefusal(mtixDir)
	if refusal == nil || refusal.ResolvedAt != "" || refusal.FileHash == "" {
		return false
	}
	fileBytes, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	if err != nil || !s.refusalPending(mtixDir, refusal, fileBytes) {
		return false
	}
	s.noticeOnce("export:"+refusal.FileHash, "mtix: .mtix/tasks.json was not rewritten because it changed on "+
		"disk and has not been imported (see mtix sync); your change is saved in the local store. "+
		resolutionFor(refusal)+"\n")
	s.logger.Debug("sync_export_skipped", "event", "sync_export_skipped", "pending_file_hash", refusal.FileHash)
	return true
}

// ForceExport rewrites .mtix/tasks.json from the local store, even while
// an auto-import of it is pending or it changed on disk, without importing
// it, and marks a pending refusal resolved (mtix sync --fix, MTIX-95.31.2).
// The changes the file on disk carried are dropped.
func (s *SyncService) ForceExport(ctx context.Context, mtixDir string) error {
	if err := s.resolveRefusal(mtixDir, ""); err != nil {
		return err
	}
	return s.exportBoard(ctx, mtixDir)
}

// ResolveRefusalByImport is called after an explicit mtix import of
// importedPath (MTIX-95.31.2). When that file is the .mtix/tasks.json on
// disk, it records the file as imported (sync.sha256) and resolves a
// refusal of it, so the export after the import rewrites the board instead
// of treating it as a changed, unimported file. An import of any other
// file changes nothing here.
func (s *SyncService) ResolveRefusalByImport(mtixDir, importedPath string) error {
	imported, err := os.ReadFile(importedPath) //nolint:gosec // an operator-supplied import path, already read by the import
	if err != nil {
		return fmt.Errorf("read the imported file: %w", err)
	}
	onDisk, err := os.ReadFile(filepath.Join(mtixDir, "tasks.json"))
	if err != nil {
		return nil // no board on disk to reconcile
	}
	if !bytes.Equal(imported, onDisk) {
		return nil
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(onDisk))
	if err := s.writeHashFile(filepath.Join(mtixDir, "data", "sync.sha256"), hash); err != nil {
		return err
	}
	return s.resolveRefusal(mtixDir, hash)
}

// resolveRefusal marks the recorded refusal resolved, when there is one
// that is unresolved and, if fileHash is not empty, is for that file.
func (s *SyncService) resolveRefusal(mtixDir, fileHash string) error {
	refusal := s.readRefusal(mtixDir)
	if refusal == nil || refusal.ResolvedAt != "" || refusal.FileHash == "" {
		return nil
	}
	if fileHash != "" && refusal.FileHash != fileHash {
		return nil
	}
	refusal.ResolvedAt = s.clock().UTC().Format(time.RFC3339)
	return s.writeRefusal(mtixDir, refusal)
}
