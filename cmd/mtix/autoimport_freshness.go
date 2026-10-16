// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// runCommandAutoImport records unverified cache freshness (FR-15.2, MTIX-149).
// A refusal has already been printed once by AutoImport (MTIX-95.31.2), so
// logging it again would duplicate the notice. JSON readers carry metadata
// through their writer. Reads and protocol startup get one stderr notice;
// mutation guidance is emitted only after its handler succeeds.
func runCommandAutoImport(cmd *cobra.Command) {
	err := app.syncSvc.AutoImport(cmd.Context(), app.mtixDir)
	switch {
	case errors.Is(err, service.ErrAutoImportLockBusy):
		app.staleReason = "sync_lock_busy"
		if app.outputScope != freshnessWrite {
			writeFreshnessNotice(cmd, "mtix: possibly stale data: sync lock busy; auto-import was skipped after a short retry; retry reads after the current owner finishes; never repeat a successful write")
		}
	case err != nil && !errors.Is(err, service.ErrAutoImportRefused):
		app.logger.Warn("auto-import failed", "error", err)
	}
}
