// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
)

// historyExportGuardCheckID is the id of the history.export rollout guard row.
const historyExportGuardCheckID = "history.export.guard"

// registerHistoryExportGuardCheck adds the history.export rollout guard row
// (MTIX-95.31.19.6, ADR-006 A3.1) when the switch is on or an older build is
// detected (or the check cannot run); otherwise the row is omitted. PASS when no older build is detected, WARN
// with the exact fix when one is and the switch is off, FAIL when one is and
// the switch is on. The detail always states what no signal can see, and the
// data object carries the blockers and minimum version for --json.
func registerHistoryExportGuardCheck(svc *service.DoctorService) error {
	if app.historyGuard == nil || app.configSvc == nil {
		return nil
	}
	on := app.configSvc.HistoryExport()
	rep, err := app.historyGuard.Evaluate(context.Background())
	if err == nil && rep.Clear() && !on {
		return nil // nothing to report; the row would only change a board that never uses the feature
	}
	row := historyExportGuardRow(rep, on, err)
	return svc.Register(service.DoctorCheck{ID: historyExportGuardCheckID, Run: func(context.Context) service.DoctorResult { return row }})
}

func historyExportGuardRow(rep service.HistoryGuardReport, on bool, err error) service.DoctorResult {
	data := map[string]any{"switch_on": on, "min_version": service.FirstHistoryVersion,
		"blockers": rep.Blockers, "window_days": int(service.HistoryGuardWindow / (24 * time.Hour))}
	if err != nil {
		status := service.DoctorWarn
		if on {
			status = service.DoctorFail // enabled but unverifiable
		}
		return service.DoctorResult{Status: status, Data: data,
			Detail: fmt.Sprintf("could not check whether an older mtix touches the board: %v", err),
			Fix:    "fix the error above and run mtix doctor again; mtix config set history.export true is refused until the check can run"}
	}
	if rep.Clear() {
		return service.DoctorResult{Status: service.DoctorPass, Data: data,
			Detail: "every registration and daemon seen in the last 30 days holds task history; " + service.HistoryGuardBlindSpot}
	}
	row := service.DoctorResult{Status: service.DoctorWarn, Data: data, Detail: rep.Refusal(),
		Fix: fmt.Sprintf("upgrade mtix to %s or later on each machine and daemon listed, restart the daemon and let it write the board once, "+
			"then run mtix doctor until this row passes before mtix config set history.export true", service.FirstHistoryVersion)}
	if on {
		row.Status = service.DoctorFail
		row.Fix = "mtix config set history.export false (the file returns to 2.0.0 at the next write), then " + row.Fix
	}
	return row
}
