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

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// refreshDBHash writes the conflict baseline (sync-db.sha256) for the store
// as it is after an auto-import (MTIX-95.31.2), so the next changed
// tasks.json is not reported as a conflict (FR-15.2h). A failure is logged:
// the next pull is then reported as a conflict, the safe side.
func (s *SyncService) refreshDBHash(ctx context.Context, mtixDir string) {
	dbHash, err := s.computeDBHash(ctx)
	if err == nil {
		err = os.WriteFile(filepath.Join(mtixDir, "data", "sync-db.sha256"), []byte(dbHash), 0o644)
	}
	if err != nil {
		s.logger.Warn("could not refresh the conflict baseline after auto-import", "error", err)
	}
}

// computeDBHash exports the current DB state and computes its SHA-256 hash
// (exportHash). A store that cannot be exported is an error, never an
// empty hash (MTIX-95.31.1).
func (s *SyncService) computeDBHash(ctx context.Context) (string, error) {
	data, err := s.store.Export(ctx, "", "")
	if err != nil {
		return "", fmt.Errorf("export the local store: %w", err)
	}
	return exportHash(data)
}

// exportHash computes the SHA-256 hash of an export's content. The
// ExportedAt timestamp is left out, so the hash reflects only data content,
// not when the export was generated. Without this, two exports of identical
// data in different seconds produce different hashes, causing
// false-positive conflict detection in hasConflict. data is not changed.
func exportHash(data *sqlite.ExportData) (string, error) {
	content := *data
	content.ExportedAt = ""
	jsonBytes, err := json.Marshal(&content)
	if err != nil {
		return "", fmt.Errorf("encode the local store: %w", err)
	}
	hash := sha256.Sum256(jsonBytes)
	return fmt.Sprintf("%x", hash), nil
}
