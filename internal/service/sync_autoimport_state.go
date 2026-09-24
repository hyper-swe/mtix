// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// autoImportRefusalFile is the local record of the last auto-import mtix
// refused (MTIX-95.31.2), kept next to sync.sha256 in .mtix/data, the
// directory of local state that is never committed (FR-15.4). mtix sync
// reports it, and AutoExport reads it to leave a refused tasks.json alone.
const autoImportRefusalFile = "auto-import-refusal.json"

// AutoImportRefusal is the last automatic import of a changed
// .mtix/tasks.json that mtix refused or skipped because it needs the
// user's decision (MTIX-95.31.2): a replace that would delete local data
// (FR-15.2i), a conflict (FR-15.2h), a newer schema (FR-15.2g), a local
// store that cannot be exported (MTIX-95.31.1) or a backup that could not
// be written (FR-15.2f).
type AutoImportRefusal struct {
	// RefusedAt is when the refusal happened (RFC 3339, UTC).
	RefusedAt string `json:"refused_at"`
	// FileHash is the SHA-256 of the tasks.json that was refused.
	FileHash string `json:"file_hash"`
	// Kind is what stopped the import: lossy, conflict, newer_schema,
	// invalid_file, unreadable_store, backup_failed or not_imported (the
	// file changed on disk and a write found it not imported, for example
	// with sync.auto_sync false). It decides the way out mtix names.
	Kind string `json:"kind"`
	// Reason says why, naming what would have been lost.
	Reason string `json:"reason"`
	// Loss lists the local data a replace import of the file would delete,
	// when that is known (a lossy refusal, or a conflict whose replace
	// would lose data, MTIX-95.31.4), so the way out mtix names says what
	// the replace option deletes.
	Loss string `json:"loss,omitempty"`
	// ResolvedAt is when the user resolved it deliberately (mtix sync
	// --fix, or mtix import of that file), or empty.
	ResolvedAt string `json:"resolved_at,omitempty"`
	// Pending is true while that tasks.json is still on disk, not imported
	// and not resolved: the refusal repeats on every command, and
	// auto-export leaves the file alone. It is computed, not stored.
	Pending bool `json:"pending"`
}

// AutoImportState is the automatic import state mtix sync reports
// (MTIX-95.31.2).
type AutoImportState struct {
	// Enabled is false when sync.auto_sync is false: a changed tasks.json
	// is then imported automatically only into an empty store, though it
	// is still exported.
	Enabled bool `json:"enabled"`
	// Setting is the configured sync.auto_sync value, as written.
	Setting string `json:"setting"`
	// SettingError says why Setting is not true or false (the default,
	// on, then applies), or is empty.
	SettingError string `json:"setting_error,omitempty"`
	// LastRefusal is the last refused auto-import, or nil.
	LastRefusal *AutoImportRefusal `json:"last_refusal,omitempty"`
}

// recordRefusal stores the refusal of the tasks.json with hash fileHash,
// of the given kind, for mtix sync and for AutoExport, replacing the
// previous record. A failure to store it is logged, never fatal: the
// refusal itself has already been reported.
func (s *SyncService) recordRefusal(mtixDir, fileHash, kind, reason string) {
	s.recordLossyRefusal(mtixDir, fileHash, kind, reason, "")
}

// recordLossyRefusal records a refusal as recordRefusal does, with the
// local data a replace of the file would delete (loss, MTIX-95.31.4).
func (s *SyncService) recordLossyRefusal(mtixDir, fileHash, kind, reason, loss string) {
	err := s.writeRefusal(mtixDir, &AutoImportRefusal{
		RefusedAt: s.clock().UTC().Format(time.RFC3339),
		FileHash:  fileHash,
		Kind:      kind,
		Reason:    reason,
		Loss:      loss,
	})
	if err != nil {
		s.logger.Warn("could not record the auto-import refusal for mtix sync", "error", err)
	}
}

// writeRefusal writes the refusal record.
func (s *SyncService) writeRefusal(mtixDir string, refusal *AutoImportRefusal) error {
	stored := *refusal
	stored.Pending = false // computed on read, never stored
	record, err := json.MarshalIndent(&stored, "", "  ")
	if err != nil {
		return fmt.Errorf("encode the auto-import refusal record: %w", err)
	}
	path := filepath.Join(mtixDir, "data", autoImportRefusalFile)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create sync data dir: %w", err)
	}
	if err := os.WriteFile(path, record, 0o644); err != nil {
		return fmt.Errorf("write the auto-import refusal record: %w", err)
	}
	return nil
}

// autoImportState reports the sync.auto_sync switch and the last refusal,
// which is pending while the refused tasks.json (fileBytes is the current
// one) is on disk, unresolved, and its hash is not the stored, imported
// one.
func (s *SyncService) autoImportState(mtixDir string, fileBytes []byte) AutoImportState {
	state := AutoImportState{Enabled: s.AutoImportEnabled(), Setting: "true"}
	if s.autoSync != nil {
		raw, _, err := s.autoSync.AutoSyncSetting()
		state.Setting = raw
		if err != nil {
			state.SettingError = err.Error()
		}
	}
	refusal := s.readRefusal(mtixDir)
	if refusal != nil {
		refusal.Pending = s.refusalPending(mtixDir, refusal, fileBytes)
		state.LastRefusal = refusal
	}
	return state
}

// refusalPending reports whether refusal still holds for the tasks.json
// whose content is fileBytes: it was not resolved, the file is the one
// refused, and that file is not the stored, imported one.
func (s *SyncService) refusalPending(mtixDir string, refusal *AutoImportRefusal, fileBytes []byte) bool {
	if refusal.ResolvedAt != "" || refusal.FileHash == "" {
		return false
	}
	current := fmt.Sprintf("%x", sha256.Sum256(fileBytes))
	stored, err := os.ReadFile(filepath.Join(mtixDir, "data", "sync.sha256"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		s.logger.Warn("could not read the stored tasks.json hash", "error", err)
	}
	return refusal.FileHash == current && string(stored) != current
}

// readRefusal returns the recorded refusal, nil when there is none, or one
// whose reason says the record cannot be read.
func (s *SyncService) readRefusal(mtixDir string) *AutoImportRefusal {
	path := filepath.Join(mtixDir, "data", autoImportRefusalFile)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	var refusal AutoImportRefusal
	if err == nil {
		err = json.Unmarshal(raw, &refusal)
	}
	if err != nil {
		return &AutoImportRefusal{Reason: fmt.Sprintf("the refusal record %s cannot be read: %v", path, err)}
	}
	return &refusal
}
