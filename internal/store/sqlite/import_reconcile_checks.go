// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
)

// validateImportUIDs detects duplicate uids within the export and
// validates each incoming uid against the local store (ADR-003 §6, F-3),
// recording conflicts, idempotent no-ops and re-stamps in report. Any
// conflict rejects the import with ErrConflict before anything is written.
func (s *Store) validateImportUIDs(
	ctx context.Context, data *ExportData, opts ImportReconcileOptions, report *ImportReconcileReport,
) error {
	report.Conflicts = append(report.Conflicts, exportDuplicateUIDConflicts(data)...)
	if err := s.classifyAgainstLocal(ctx, data, opts, report); err != nil {
		return err
	}
	if len(report.Conflicts) > 0 {
		return fmt.Errorf("import rejected: %d uid conflict(s): %w", len(report.Conflicts), model.ErrConflict)
	}
	return nil
}

// requireConfirmation returns ErrImportConfirmationRequired, before
// anything is written, when the planned import renumbers without confirm:
// a local task (MTIX-95.31.4), or an incoming provisional node while the
// store holds nodes (ADR-003 §6).
func (s *Store) requireConfirmation(ctx context.Context, report *ImportReconcileReport, confirm bool) error {
	if confirm {
		return nil
	}
	if n := len(report.LocalRenumbers); n > 0 {
		return fmt.Errorf("%d local node(s) would be renumbered because the file holds a different task under "+
			"their id; review the report and re-run with confirmation: %w", n, ErrImportConfirmationRequired)
	}
	if len(report.Remaps) == 0 {
		return nil
	}
	empty, err := s.storeIsEmpty(ctx)
	if err != nil {
		return err
	}
	if !empty {
		return fmt.Errorf(
			"%d provisional node(s) would be renumbered in a live store; "+
				"re-run with confirmation: %w",
			len(report.Remaps), ErrImportConfirmationRequired)
	}
	return nil
}
