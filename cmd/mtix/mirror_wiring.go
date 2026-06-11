// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/service"
)

// wireMirrorExporter connects the store's on-commit hook to a debounced
// tasks.json auto-export per FR-15.3 / NFR-2.8 (MTIX-26.1). Long-running
// interfaces — the MCP server, mtix serve, and the sync daemon — never
// reach the CLI's PersistentPostRunE export, so without this hook their
// mutations would leave the mirror stale: the gap behind the 2026-05-19
// data-loss incident. Every long-running entry point MUST call this and
// defer the returned cleanup, which flushes the final pending export.
//
// Returns a no-op cleanup when sync is unavailable (no project dir).
func wireMirrorExporter(logger *slog.Logger) (cleanup func()) {
	if app.syncSvc == nil || app.mtixDir == "" || app.store == nil {
		return func() {}
	}
	scheduler := newAutoBackupScheduler(logger)
	exporter := service.NewExportDebouncer(
		func(ctx context.Context) error {
			err := app.syncSvc.AutoExport(ctx, app.mtixDir)
			// Rolling backup per FR-26.6 rides the same post-mutation
			// cadence; its failure is logged inside and never propagates.
			runAutoBackup(scheduler, logger)
			return err
		},
		logger, 0, 0,
	)
	app.store.SetOnCommit(exporter.Trigger)
	return exporter.Close
}

// newAutoBackupScheduler builds the rolling-backup scheduler for the
// current project (FR-26.6), honoring MTIX_BACKUP_INTERVAL / _RETAIN.
func newAutoBackupScheduler(logger *slog.Logger) *service.BackupScheduler {
	return service.NewBackupSchedulerFromEnv(app.store,
		filepath.Join(app.mtixDir, "data", "backups"), logger)
}

// runAutoBackup takes an interval-gated backup, logging failures —
// backups are redundancy, so trouble is loud but never fatal.
func runAutoBackup(scheduler *service.BackupScheduler, logger *slog.Logger) {
	if _, err := scheduler.MaybeBackup(context.Background()); err != nil {
		logger.Warn("automatic backup failed", "error", err)
	}
}

// maybeAutoBackup is the one-shot CLI trigger (FR-26.6): called from
// PersistentPostRunE after a mutation command, alongside auto-export.
func maybeAutoBackup() {
	if app.store == nil || app.mtixDir == "" {
		return
	}
	logger := app.logger
	if logger == nil {
		return
	}
	runAutoBackup(newAutoBackupScheduler(logger), logger)
}
