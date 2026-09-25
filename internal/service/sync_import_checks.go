// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// isOwnExport reports whether tasks.json was produced by this database's
// own export (or a sibling agent's export into it): its hash matches the
// meta table's last_export_hash (FR-15.2, step 4b of AutoImport). The
// import is then redundant, so it is skipped and sync.sha256 is updated to
// stop re-checking on the next command; this avoids a redundant replace
// import and the thundering herd of multiple agents on one machine.
func (s *SyncService) isOwnExport(ctx context.Context, hashPath, fileHash string) bool {
	var lastExportHash string
	// Read the hash AutoExport stored for this database's last export.
	queryErr := s.store.QueryRow(ctx,
		"SELECT value FROM meta WHERE key = 'last_export_hash'",
	).Scan(&lastExportHash)
	if queryErr == nil && lastExportHash == fileHash {
		if hashErr := s.writeHashFile(hashPath, fileHash); hashErr != nil {
			s.logger.Warn("failed to update sync hash after export-match skip", "error", hashErr)
		}
		return true
	}
	if queryErr != nil && !errors.Is(queryErr, sql.ErrNoRows) {
		// Genuine DB error (not "key not found") — log for audit trail
		// per NASA-STD-8739.8 §7.2, then proceed with import as fail-safe.
		s.logger.Warn("failed to read last_export_hash from meta, proceeding with import", "error", queryErr)
	}
	return false
}

// decodeTasksFile parses tasks.json bytes and checks the schema version
// (FR-15.2g). It returns nil (no error) when the file is skipped with a
// warning: not an export object, or a newer schema than this build reads.
func (s *SyncService) decodeTasksFile(data []byte, mtixDir, fileHash string) (*sqlite.ExportData, error) {
	tasksPath := filepath.Join(mtixDir, "tasks.json")
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && trimmed[0] != '{' {
		s.logger.Warn("tasks.json is not in mtix ExportData format (expected JSON object, got array or other type)",
			"path", tasksPath,
			"fix", "run 'mtix sync --fix' to re-export it from the database")
		s.recordRefusal(mtixDir, fileHash, refusalInvalidFile,
			"tasks.json is not an mtix export (expected a JSON object)")
		return nil, nil
	}

	var exportData sqlite.ExportData
	if err := json.Unmarshal(data, &exportData); err != nil {
		parseErr := fmt.Errorf("parse tasks.json: %w", err)
		s.recordRefusal(mtixDir, fileHash, refusalInvalidFile, parseErr.Error()) // MTIX-95.31.2
		return nil, parseErr
	}

	schemaVer := exportData.SchemaVersion
	if schemaVer == "" {
		schemaVer = "1.0.0"
	}
	if schemaErr := CheckSchemaVersion(schemaVer); schemaErr != nil {
		s.logger.Error("tasks.json schema version is newer than supported — upgrade mtix",
			"file_version", schemaVer,
			"supported_version", supportedSchemaVersion)
		s.recordRefusal(mtixDir, fileHash, refusalNewerSchema, schemaErr.Error())
		return nil, nil
	}
	return &exportData, nil
}
