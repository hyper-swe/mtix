// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"

	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// matchingPublishedExport compares actual valid disk content, not a cached
// baseline (FR-15.3, MTIX-150). Missing, invalid and older-form files still need
// publication; automatic export's ownership/refusal guard runs before this.
func matchingPublishedExport(path string, current *sqlite.ExportData) ([]byte, error) {
	// #nosec G703 -- fixed tasks.json under the caller-selected local project; no received content controls this path.
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read published export: %w", err)
	}
	previous, err := sqlite.DecodeExportData(bytes.NewReader(raw))
	if err != nil || previous.SchemaVersion != current.SchemaVersion {
		return nil, nil
	}
	if validationErr := sqlite.ValidateExport(previous); validationErr != nil {
		return nil, nil
	}
	oldHash, err := exportHash(previous, formCurrent)
	if err != nil {
		return nil, fmt.Errorf("hash published export content: %w", err)
	}
	newHash, err := exportHash(current, formCurrent)
	if err != nil {
		return nil, fmt.Errorf("hash current export content: %w", err)
	}
	if oldHash != newHash {
		return nil, nil
	}
	return raw, nil
}

// recordUnchangedExport retains raw file-hash semantics and repairs metadata
// before exportBoardPass retires its pending cutoff (FR-15.3d, MTIX-150).
func (s *SyncService) recordUnchangedExport(ctx context.Context, dir string, raw []byte, current *sqlite.ExportData) error {
	fileHash := fmt.Sprintf("%x", sha256.Sum256(raw))
	if err := s.publishExportHashes(ctx, dir, fileHash, current); err != nil {
		return err
	}
	return syncExportPublicationWithSync(dir, s.syncDirectory)
}
