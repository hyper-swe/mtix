// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"runtime"

	"github.com/hyper-swe/mtix/internal/attach"
	"github.com/hyper-swe/mtix/internal/oplocal"
)

// productionAttachRegistry lists the release-defined artifacts this build can
// rewrite. Only the shim ships now; harness glue writers register with their
// harness integrations.
func productionAttachRegistry(st *oplocal.State, current int) (*attach.Registry, error) {
	return attach.NewRegistry(current, attach.NewShimWriter(st, current, attach.ValidateShimTarget))
}

// refreshAttachArtifacts rewrites release-defined attach artifacts that are
// stale or marked for refresh, without a terminal (ADR-007 §6.5). It runs
// after every command. A sandboxed command cannot write operator state, so
// its refresh fails and the next unsandboxed command or mtix doctor --fix
// does the work; the failure is logged at debug level only, because a
// sandboxed agent would otherwise see it on every command. Grants are never
// touched: the registry holds no grant writer.
func refreshAttachArtifacts(ctx context.Context, logger *slog.Logger) {
	if runtime.GOOS == "windows" {
		return // no shim and no built-in wakes on Windows (ADR-007 §2.10)
	}
	st, err := operatorState()
	if err != nil {
		logAttachRefresh(logger, "operator state unavailable", err)
		return
	}
	reg, err := productionAttachRegistry(st, attach.Protocol)
	if err != nil {
		logAttachRefresh(logger, "attach registry invalid", err)
		return
	}
	report, err := reg.Refresh(ctx, st)
	if err != nil {
		logAttachRefresh(logger, "attach refresh incomplete", err)
	}
	if len(report.Rewritten) > 0 && logger != nil {
		logger.Info("refreshed attach artifacts", "artifacts", report.Rewritten, "protocol", attach.Protocol)
	}
}

func logAttachRefresh(logger *slog.Logger, msg string, err error) {
	if logger != nil {
		logger.Debug(msg, "error", err)
	}
}
