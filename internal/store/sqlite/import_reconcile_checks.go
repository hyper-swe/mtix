// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package sqlite

import (
	"context"
	"errors"
	"fmt"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/sync/clock"
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
	return rejectConflicts(report)
}

// rejectConflicts rejects the import with ErrConflict, before anything is
// written, when report holds a uid conflict (ADR-003 §6, F-3).
func rejectConflicts(report *ImportReconcileReport) error {
	if len(report.Conflicts) > 0 {
		return fmt.Errorf("import rejected: %d uid conflict(s): %w", len(report.Conflicts), model.ErrConflict)
	}
	return nil
}

// prepareWrite readies a reconciled import for writing: it recomputes the
// checksum over content the reconcile rewrote (ADR-003 §6) and, when the
// caller set BeforeWrite, runs every check Import makes first (the file's
// count, checksum and times, and the zero-node guard), then a dry run of
// the import (countImportChanges), and runs BeforeWrite only when the
// import will change something (MTIX-95.31.4). So a file that would be
// refused, and an import that changes nothing, cost no backup. It returns
// the import options the write takes: after a dry run, IfStoreUnchanged
// with the checksum the dry run read, so a write committed after the dry
// run (or by BeforeWrite) makes the import write nothing.
func (s *Store) prepareWrite(ctx context.Context, data *ExportData, opts ImportReconcileOptions,
	report *ImportReconcileReport, moves []localRenumber) ([]ImportOption, error) {
	if len(report.Remaps) > 0 || len(report.Renamed) > 0 {
		if err := RecomputeExportChecksum(data); err != nil {
			return nil, fmt.Errorf("recompute checksum after reconcile: %w", err)
		}
	}
	writeOpts := []ImportOption{renumberLocalFirst(moves)}
	if opts.BeforeWrite == nil {
		return writeOpts, nil
	}
	if err := ValidateExport(data); err != nil {
		return nil, err
	}
	if err := s.refuseEmptyImport(ctx, data, opts.Force); err != nil {
		return nil, err
	}
	changes, checksum, err := s.countImportChanges(ctx, data, opts.Mode, moves)
	if err != nil {
		return nil, err
	}
	writeOpts = append(writeOpts, IfStoreUnchanged(checksum))
	if changes == 0 {
		return writeOpts, nil
	}
	if err := opts.BeforeWrite(); err != nil {
		return nil, fmt.Errorf("before the import writes: %w", err)
	}
	return writeOpts, nil
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

// classifyAgainstLocal validates each incoming uid against the local store
// (ADR-003 §6, F-3). For each node carrying a uid that already exists locally:
// an identical display_path is an idempotent no-op (counted, left untouched); a
// different display_path is a collision — rejected, or, with ForceRename,
// re-stamped on the import node with a freshly minted local uid. Empty uids are
// skipped (no shared identity to validate). In a merge without ForceRename a
// different display_path is the same task renumbered elsewhere
// (MTIX-95.31.4): planLocalRenumbers moves the local node to it, or reports
// the collision when it cannot (another parent).
func (s *Store) classifyAgainstLocal(
	ctx context.Context,
	data *ExportData,
	opts ImportReconcileOptions,
	report *ImportReconcileReport,
) error {
	for i := range data.Nodes {
		n := &data.Nodes[i]
		if n.UID == "" {
			continue
		}
		localPath, err := s.ResolveDisplayPathByUID(ctx, n.UID)
		if errors.Is(err, model.ErrNotFound) {
			continue // uid is new to this store — nothing to reconcile.
		}
		if err != nil {
			return fmt.Errorf("validate import uid %s: %w", n.UID, err)
		}
		if localPath == n.ID {
			report.Idempotent++ // identical uid + display_path — a no-op.
			continue
		}
		if opts.Mode == ImportModeMerge && !opts.ForceRename {
			continue // MTIX-95.31.4: the same task, renumbered elsewhere: planLocalRenumbers moves it or rejects it
		}
		if !opts.ForceRename {
			report.Conflicts = append(report.Conflicts, ImportUIDConflict{
				UID:        n.UID,
				ImportPath: n.ID,
				LocalPath:  localPath,
				Kind:       ConflictLocalUIDMismatch,
			})
			continue
		}
		// Force-rename: re-stamp the IMPORT node with a fresh local uid so it
		// stops colliding with the local node (ADR-003 §6).
		freshUID, mintErr := clock.NewEventID()
		if mintErr != nil {
			return fmt.Errorf("mint replacement uid for %s: %w", n.ID, mintErr)
		}
		report.Renamed = append(report.Renamed, ImportRemapEntry{
			UID: n.UID, OldPath: n.ID, NewPath: n.ID,
		})
		n.UID = freshUID
	}
	return nil
}
