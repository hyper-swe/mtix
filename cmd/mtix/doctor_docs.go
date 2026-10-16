// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/hyper-swe/mtix/internal/docs"
	"github.com/hyper-swe/mtix/internal/service"
)

// documentFreshnessCheck owns no repair capability: the command prints the
// board-root operation rather than letting --fix write project instructions.
func documentFreshnessCheck(root string) service.DoctorCheck {
	return service.DoctorCheck{ID: "docs.stale", Run: func(context.Context) service.DoctorResult {
		fix := "mtix -C '" + strings.ReplaceAll(root, "'", "'\"'\"'") + "' docs generate"
		data := projectDocTemplateData(filepath.Join(root, ".mtix"))
		gen, err := docs.NewEmbeddedGenerator(filepath.Join(root, ".mtix", "docs"), data, slog.Default())
		if err != nil {
			return unavailableProjectDocs(fix)
		}
		reasons, err := gen.CheckProjectBriefing(root)
		if err != nil {
			return unavailableProjectDocs(fix)
		}
		if len(reasons) > 0 {
			return service.DoctorResult{Status: service.DoctorWarn, Detail: strings.Join(reasons, "; "), Fix: fix}
		}
		return service.DoctorResult{Status: service.DoctorPass, Detail: "MTIX.md and native instruction pointers are current"}
	}}
}

func unavailableProjectDocs(fix string) service.DoctorResult {
	return service.DoctorResult{Status: service.DoctorWarn,
		Detail: "Project instructions could not be inspected; check document file types and read permissions", Fix: fix}
}
