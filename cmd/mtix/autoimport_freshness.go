// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"errors"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/hyper-swe/mtix/internal/service"
)

// runCommandAutoImport records unverified cache freshness (FR-15.2, MTIX-149).
// A refusal has already been printed once by AutoImport (MTIX-95.31.2), so
// logging it again would duplicate the notice. JSON readers carry metadata
// through their writer. Every caller gets one stderr notice, including protocol
// startup commands whose stdout is not CLI JSON even when --json is supplied.
func runCommandAutoImport(cmd *cobra.Command) {
	err := app.syncSvc.AutoImport(cmd.Context(), app.mtixDir)
	switch {
	case errors.Is(err, service.ErrAutoImportLockBusy):
		app.staleReason = "sync_lock_busy"
		fmt.Fprintln(cmd.ErrOrStderr(), "mtix: possibly stale data: sync lock busy; auto-import was skipped after a short retry; retry this command after the current owner finishes")
	case err != nil && !errors.Is(err, service.ErrAutoImportRefused):
		app.logger.Warn("auto-import failed", "error", err)
	}
}
