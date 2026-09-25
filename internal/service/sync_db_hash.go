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

// baselineForm is a form of the local store's export that a conflict
// baseline (sync-db.sha256) may have been hashed over (MTIX-95.31.11,
// FR-15.2h). The zero value is the form this version writes; the others
// are flags that combine.
type baselineForm uint8

const (
	// formCurrent is the export this version writes (schema 2.0.0).
	formCurrent baselineForm = 0
	// formWithoutBackfillUIDs leaves out every backfill uid: the baseline
	// was written before the open-time backfill (MTIX-95.31.9) gave the
	// tasks without a uid one.
	formWithoutBackfillUIDs baselineForm = 1 << 0
	// form100 is the 1.0.0 export mtix 0.5.3 and earlier wrote and hashed.
	form100 baselineForm = 1 << 1
)

// olderBaselineForms are the forms, other than the current one, a baseline
// written by mtix 0.5.3 or earlier, or by this version before backfill
// uids were minted, is in: the store's export without its backfill uids,
// in the 1.0.0 form, and both (MTIX-95.31.11).
func olderBaselineForms() []baselineForm {
	return []baselineForm{formWithoutBackfillUIDs, form100, form100 | formWithoutBackfillUIDs}
}

// String names the form in logs: "current", "1.0.0", and either followed
// by " without backfill uids".
func (f baselineForm) String() string {
	name := "current"
	if f&form100 != 0 {
		name = "1.0.0"
	}
	if f&formWithoutBackfillUIDs != 0 {
		name += " without backfill uids"
	}
	return name
}

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
	return exportHash(data, formCurrent)
}

// exportHash computes the SHA-256 hash of an export's content in the given
// form (MTIX-95.31.11): formCurrent hashes the export as it is, the
// conflict baseline this version writes. The ExportedAt timestamp is left
// out, so the hash reflects only data content, not when the export was
// generated. Without this, two exports of identical data in different
// seconds produce different hashes, causing false-positive conflict
// detection in hasConflict. An older form (olderBaselineForms) first
// leaves out every backfill uid (sqlite.WithoutBackfillUIDs), for a
// baseline written before the open-time backfill minted them, and/or turns
// the export into the 1.0.0 form mtix 0.5.3 hashed (sqlite.Schema100Form),
// in that order, each with its checksum computed again, so hasConflict can
// recognize a baseline an older build wrote for the same store. data is
// not changed.
func exportHash(data *sqlite.ExportData, form baselineForm) (string, error) {
	content := *data
	content.ExportedAt = ""
	if form&formWithoutBackfillUIDs != 0 {
		older, err := sqlite.WithoutBackfillUIDs(&content)
		if err != nil {
			return "", fmt.Errorf("the local store without its backfill uids: %w", err)
		}
		content = *older
	}
	if form&form100 != 0 {
		older, err := sqlite.Schema100Form(&content)
		if err != nil {
			return "", fmt.Errorf("the local store in the 1.0.0 form: %w", err)
		}
		content = *older
	}
	jsonBytes, err := json.Marshal(&content)
	if err != nil {
		return "", fmt.Errorf("encode the local store: %w", err)
	}
	hash := sha256.Sum256(jsonBytes)
	return fmt.Sprintf("%x", hash), nil
}

// matchOlderBaseline returns the older form (olderBaselineForms) in which
// the export local hashes to stored, the conflict baseline, and whether
// there is one (MTIX-95.31.11). hasConflict compares the current form
// first. A store changed since its baseline was written matches no older
// form, unless the change is only to fields that form lacks.
func matchOlderBaseline(local *sqlite.ExportData, stored string) (baselineForm, bool, error) {
	for _, form := range olderBaselineForms() {
		hash, err := exportHash(local, form)
		if err != nil {
			return formCurrent, false, fmt.Errorf("hash the local store in the %s form: %w", form, err)
		}
		if hash == stored {
			return form, true, nil
		}
	}
	return formCurrent, false, nil
}

// upgradeBaseline rewrites the conflict baseline (sync-db.sha256), which
// matched the unchanged local store in an older form, with current, the
// store's hash in the current form, and logs it (MTIX-95.31.11, FR-15.2h).
// From then on the baseline is compared exactly, so a change to a field
// the older form lacks counts as a change too. A failed write is logged:
// the next command recognizes the older baseline again.
func (s *SyncService) upgradeBaseline(mtixDir, current string, form baselineForm) {
	if err := os.WriteFile(filepath.Join(mtixDir, "data", "sync-db.sha256"), []byte(current), 0o644); err != nil {
		s.logger.Warn("could not rewrite the conflict baseline in the current form", "error", err)
		return
	}
	s.logger.Info("sync_baseline_upgraded", "event", "sync_baseline_upgraded", "from_form", form.String())
}
