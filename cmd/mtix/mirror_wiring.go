// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"

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
	exporter := service.NewExportDebouncer(
		func(ctx context.Context) error {
			return app.syncSvc.AutoExport(ctx, app.mtixDir)
		},
		logger, 0, 0,
	)
	app.store.SetOnCommit(exporter.Trigger)
	return exporter.Close
}
