// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/model"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// statusRepairBackupLayout is the UTC time in a status repair backup's file
// name, pre-repair-status-<time>.db (MTIX-95.6).
const statusRepairBackupLayout = "20060102T150405Z"

// StatusRepairDiff is one node whose workflow state differs from its local
// workflow events, as the store derives it (MTIX-95.6).
type StatusRepairDiff = sqlite.StatusRepairDiff

// StatusRepairReport is the result of SyncService.RepairStatus (MTIX-95.6).
// Differences lists every node whose workflow state differs from its local
// workflow events when the run started, each classified as a replay, a
// derived fix or flagged for review. With Apply, Backup is the verified
// backup taken before the first write (empty when nothing was to be
// written), Repaired lists the nodes repaired, each with the differences it
// had when it was repaired, and Skipped the flagged nodes left alone because
// Force was not set.
type StatusRepairReport struct {
	Apply       bool               `json:"apply"`
	Force       bool               `json:"force"`
	Differences []StatusRepairDiff `json:"differences"`
	Backup      string             `json:"backup,omitempty"`
	Repaired    []StatusRepairDiff `json:"repaired,omitempty"`
	Skipped     []StatusRepairDiff `json:"skipped,omitempty"`
}

// StatusRepairOptions are the modes of SyncService.RepairStatus (MTIX-95.6).
type StatusRepairOptions struct {
	Apply bool // repair the listed nodes (default: dry run)
	Force bool // also repair nodes flagged for review; needs Apply
}

// RepairStatus re-derives every live node's workflow state from the local
// sync event log with the ingest winner rule and reports the nodes whose
// stored state differs: `mtix sync repair --status` (MTIX-95.6; ADR-006
// §5.3). Without Apply it writes nothing. Force without Apply is
// ErrInvalidInput.
//
// With Apply, and at least one node to repair (a replay, a derived fix, or a
// flagged node when Force is set), it first writes a verified backup of the
// database to <mtixDir>/data/backups/pre-repair-status-<UTC time>.db
// (Store.Backup), and fails before any write if it cannot. It then repairs
// each listed node in its own transaction (Store.RepairNodeStatus), as
// author; a flagged node is skipped unless Force is set. On a repair error
// it returns the report so far with the error; the nodes already repaired
// stay repaired.
func (s *SyncService) RepairStatus(ctx context.Context, mtixDir string, opts StatusRepairOptions, author string) (*StatusRepairReport, error) {
	if opts.Force && !opts.Apply {
		return nil, fmt.Errorf("status repair: --force needs --apply: %w", model.ErrInvalidInput)
	}
	diffs, err := s.store.StatusRepairDiffs(ctx)
	if err != nil {
		return nil, fmt.Errorf("status repair: %w", err)
	}
	report := &StatusRepairReport{Apply: opts.Apply, Force: opts.Force, Differences: diffs}
	if !opts.Apply {
		return report, nil
	}
	if !anyToRepair(diffs, opts.Force) {
		report.Skipped = diffs
		return report, nil
	}
	if report.Backup, err = s.backupBeforeStatusRepair(ctx, mtixDir); err != nil {
		return nil, err
	}
	for _, d := range diffs {
		listed, applied, repairErr := s.store.RepairNodeStatus(ctx, d.NodeID, author, opts.Force)
		if repairErr != nil {
			return report, fmt.Errorf("status repair: %w", repairErr)
		}
		switch {
		case listed == nil:
		case applied:
			report.Repaired = append(report.Repaired, *listed)
		default:
			report.Skipped = append(report.Skipped, *listed)
		}
	}
	s.logger.Info("status_repair_completed", "event", "status_repair_completed",
		"differences", len(diffs), "repaired", len(report.Repaired), "skipped", len(report.Skipped),
		"backup", report.Backup)
	return report, nil
}

// anyToRepair reports whether apply would write anything: a difference that
// is not flagged, or any difference with force (MTIX-95.6).
func anyToRepair(diffs []StatusRepairDiff, force bool) bool {
	for _, d := range diffs {
		if force || !d.Flagged {
			return true
		}
	}
	return false
}

// backupBeforeStatusRepair writes the verified backup a status repair takes
// before its first write and returns its path (MTIX-95.6). Store.Backup
// copies the database with VACUUM INTO and verifies the copy with
// quick_check, deleting it and failing when it does not verify. An existing
// backup is never overwritten: when pre-repair-status-<UTC time>.db is taken
// (two repairs in one second), the name gets a -2, -3, ... suffix.
func (s *SyncService) backupBeforeStatusRepair(ctx context.Context, mtixDir string) (string, error) {
	dir := filepath.Join(mtixDir, "data", "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("status repair: create backups directory: %w", err)
	}
	path, err := freeBackupPath(dir, "pre-repair-status-"+s.clock().UTC().Format(statusRepairBackupLayout))
	if err != nil {
		return "", err
	}
	result, err := s.store.Backup(ctx, path)
	if err != nil {
		return "", fmt.Errorf("status repair: backup before repair: %w", err)
	}
	return result.Path, nil
}

// freeBackupPath returns dir/base.db, or dir/base-N.db for the lowest N from 2
// whose name is not taken.
func freeBackupPath(dir, base string) (string, error) {
	for n := 1; n < 1000; n++ {
		name := base + ".db"
		if n > 1 {
			name = fmt.Sprintf("%s-%d.db", base, n)
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path, nil
		} else if err != nil {
			return "", fmt.Errorf("status repair: check backup name %s: %w", name, err)
		}
	}
	return "", fmt.Errorf("status repair: no free backup name for %s in %s", base, dir)
}
