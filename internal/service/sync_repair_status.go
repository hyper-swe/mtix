// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package service

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

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
// workflow events when the run started. With Apply, Backup is the verified
// backup taken before the first write (empty when there was nothing to
// repair) and Repaired lists the nodes repaired, each with the differences
// it had when it was repaired.
type StatusRepairReport struct {
	Apply       bool               `json:"apply"`
	Differences []StatusRepairDiff `json:"differences"`
	Backup      string             `json:"backup,omitempty"`
	Repaired    []StatusRepairDiff `json:"repaired,omitempty"`
}

// RepairStatus re-derives every live node's workflow state from the local
// sync event log with the ingest winner rule and reports the nodes whose
// stored state differs: `mtix sync repair --status` (MTIX-95.6; ADR-006
// §5.3). Without apply it writes nothing.
//
// With apply, and at least one difference, it first writes a verified backup
// of the database to <mtixDir>/data/backups/pre-repair-status-<UTC time>.db
// (Store.Backup), and fails before any write if it cannot. It then repairs
// each listed node in its own transaction (Store.RepairNodeStatus), which
// records an activity entry and emits a transition_status event with the
// reason "sync repair" as author. On a repair error it returns the report so
// far with the error; the nodes already repaired stay repaired.
func (s *SyncService) RepairStatus(ctx context.Context, mtixDir string, apply bool, author string) (*StatusRepairReport, error) {
	diffs, err := s.store.StatusRepairDiffs(ctx)
	if err != nil {
		return nil, fmt.Errorf("status repair: %w", err)
	}
	report := &StatusRepairReport{Apply: apply, Differences: diffs}
	if !apply || len(diffs) == 0 {
		return report, nil
	}
	backup, err := s.backupBeforeStatusRepair(ctx, mtixDir)
	if err != nil {
		return nil, err
	}
	report.Backup = backup
	for _, d := range diffs {
		repaired, repairErr := s.store.RepairNodeStatus(ctx, d.NodeID, author)
		if repairErr != nil {
			return report, fmt.Errorf("status repair: %w", repairErr)
		}
		if repaired != nil {
			report.Repaired = append(report.Repaired, *repaired)
		}
	}
	s.logger.Info("status_repair_completed", "event", "status_repair_completed",
		"differences", len(diffs), "repaired", len(report.Repaired), "backup", backup)
	return report, nil
}

// backupBeforeStatusRepair writes the verified backup a status repair takes
// before its first write and returns its path (MTIX-95.6). Store.Backup
// copies the database with VACUUM INTO and verifies the copy with
// quick_check, deleting it and failing when it does not verify.
func (s *SyncService) backupBeforeStatusRepair(ctx context.Context, mtixDir string) (string, error) {
	dir := filepath.Join(mtixDir, "data", "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("status repair: create backups directory: %w", err)
	}
	path := filepath.Join(dir, "pre-repair-status-"+s.clock().UTC().Format(statusRepairBackupLayout)+".db")
	result, err := s.store.Backup(ctx, path)
	if err != nil {
		return "", fmt.Errorf("status repair: backup before repair: %w", err)
	}
	return result.Path, nil
}
