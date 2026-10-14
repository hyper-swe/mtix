// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"strings"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/postgres/transport"
)

type syncDoctorCollection struct {
	DSN        string
	DSNErr     error
	Postgres   bool
	Relay      bool
	Options    transport.Options
	Inspectors []func(string) (string, bool, error)
}

func configuredDoctorCollection() syncDoctorCollection {
	dsn, err := resolveSyncDSN(nil)
	return syncDoctorCollection{DSN: dsn, DSNErr: err, Postgres: !errors.Is(err, transport.ErrDSNNotConfigured), Relay: relayDir() != ""}
}

// collectSyncDoctor is shared with the legacy report renderer. Applicability is
// selected before any hub check runs; legacy callers explicitly select both families.
func collectSyncDoctor(ctx context.Context, req syncDoctorCollection) DoctorReport {
	report := DoctorReport{OverallPass: true}
	var hubReady bool
	if req.Postgres {
		report, hubReady = appendHubReadyChecks(ctx, report, req.DSN, req.DSNErr, req.Options)
	}
	report = appendLocalChecks(ctx, report)
	report = appendOperatorPlacementCheck(report)
	report = appendDoctorCheck(report, checkDataFilesystem(req.Inspectors))
	report = appendBoardIdentityChecks(report)
	report = appendRegistrationChecks(ctx, report)
	report = appendAlertsCheck(ctx, report)
	if req.Postgres {
		report = appendDoctorCheck(report, checkHubObjects(ctx, req.DSN, hubReady, req.Options))
		report = appendDoctorCheck(report, checkHubPrivileges(ctx, req.DSN, hubReady, req.Options))
	}
	report = scrubDoctorReport(report)
	if req.Relay {
		report = appendRelayChecks(ctx, report)
	}
	return report
}

// doctorSyncRows keeps every collected check, including checks registered by
// later releases. IDs retain the legacy name's words with a sync namespace.
func doctorSyncRows(report DoctorReport) []service.DoctorResult {
	rows := make([]service.DoctorResult, 0, len(report.Checks))
	for _, check := range report.Checks {
		status := service.DoctorPass
		if !check.Pass {
			status = service.DoctorFail
		} else if check.Warn {
			status = service.DoctorWarn
		}
		id := strings.Join(strings.Fields(strings.ToLower(check.Name)), "-")
		if !strings.HasPrefix(id, "sync.") {
			id = "sync." + id
		}
		rows = append(rows, service.DoctorResult{ID: id, Status: status, Detail: check.Detail, Fix: check.Fix})
	}
	return rows
}
