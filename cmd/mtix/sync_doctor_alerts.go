// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func appendAlertsCheck(ctx context.Context, report DoctorReport) DoctorReport {
	check := DoctorCheck{Name: "alerts.recent", Pass: true, Detail: "no recent operator alerts"}
	env, err := oplocal.FromProcess()
	if err != nil {
		check.Pass = false
		check.Detail = "operator environment is unavailable"
		check.Fix = "mtix hooks status --json"
		return appendDoctorCheck(report, check)
	}
	rows, err := service.NewAlerts(oplocal.New(env), time.Now).Recent(ctx, 20)
	if err != nil {
		check.Pass = false
		check.Detail = err.Error()
		check.Fix = "Preserve operator state; run mtix hooks status --json and correct the reported path or access rules."
		return appendDoctorCheck(report, check)
	}
	check.Recent = rows
	check.Warn = len(rows) > 0
	if check.Warn {
		details := make([]string, 0, len(rows))
		for _, row := range rows {
			details = append(details, fmt.Sprintf("%s %s count %d: %s; fix: %s", row.Agent, row.Class, row.Count, row.Detail, row.Fix))
		}
		check.Detail = "latest 20 operator alerts (at most): " + strings.Join(details, "; ")
	}
	return appendDoctorCheck(report, check)
}

func scrubRecentAlerts(rows []oplocal.Alert) []oplocal.Alert {
	if rows == nil {
		return nil
	}
	result := append([]oplocal.Alert{}, rows...)
	for i := range result {
		result[i].Agent = scrubSyncText(result[i].Agent)
		result[i].Detail = scrubSyncText(result[i].Detail)
		result[i].Fix = scrubSyncText(result[i].Fix)
	}
	return result
}
