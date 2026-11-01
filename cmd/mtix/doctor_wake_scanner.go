// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"strconv"

	"github.com/hyper-swe/mtix/internal/service"
)

// registerWakeScannerDoctor adds the wake.scanner row: whether this host's triggers run the
// wake scan and what it has marked (ADR-007 section 2.7, MTIX-112.17.37). It reads only.
func registerWakeScannerDoctor(svc *service.DoctorService) error {
	return svc.Register(service.DoctorCheck{ID: "wake.scanner", Run: wakeScannerDoctorRow})
}

func wakeScannerDoctorRow(ctx context.Context) service.DoctorResult {
	if app.store == nil {
		return service.DoctorResult{Status: service.DoctorInfo, Detail: "No board is open, so the wake scanner state is not read."}
	}
	host, err := existingRegistrationDoctorHost()
	if err != nil {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: "Wake scanner state is unreadable: " + err.Error(), Fix: "Check the operator state directory is readable and owned by you; then run mtix doctor."}
	}
	if host.HostID == "" {
		return service.DoctorResult{Status: service.DoctorInfo, Detail: "Wake scanner idle: this host has no registered session identity yet; register a session so it can be scanned.", Fix: "mtix agent register <agent-id>", Data: map[string]any{"active": false, "registrations": 0, "stale": 0}}
	}
	rows, err := app.store.ListRegistrations(ctx, host.HostID, host.UID)
	if err != nil {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: "Wake scanner registrations are unreadable: " + err.Error(), Fix: "mtix agent list --registrations"}
	}
	var stale []string
	for i := range rows {
		if rows[i].StaleSince != "" {
			stale = append(stale, rows[i].AgentID)
		}
	}
	data := map[string]any{"active": true, "registrations": len(rows), "stale": len(stale), "stale_agents": stale, "delivery": "inbox-only"}
	detail := "Wake scanner active on mutation commands and the dispatcher: " + strconv.Itoa(len(rows)) + " registration(s), " + strconv.Itoa(len(stale)) + " stale. Events for a stale session stay in its inbox."
	if len(stale) > 0 {
		return service.DoctorResult{Status: service.DoctorWarn, Detail: detail, Fix: "mtix agent unregister " + stale[0] + " --force", Data: data}
	}
	return service.DoctorResult{Status: service.DoctorInfo, Detail: detail, Data: data}
}
