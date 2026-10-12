// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

// Doctor checks for session registrations: agents.stale and agents.mcp-only (ADR-007 §7).

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/hyper-swe/mtix/internal/service"
	"github.com/hyper-swe/mtix/internal/store/sqlite"
)

// appendRegistrationChecks expires registrations unseen for 24 hours whose
// session is gone, then reports dead rows and MCP-only rows as warnings with
// the exact fix. A host without an operator directory reports nothing.
func appendRegistrationChecks(ctx context.Context, report DoctorReport) DoctorReport {
	if app.store == nil {
		return report
	}
	svc, err := newRegistrationService()
	if err != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "agents.stale", Pass: true, Warn: true,
			Detail: "registrations not checked: " + err.Error()})
	}
	expired, err := svc.ExpireStale(ctx)
	if err != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "agents.stale", Detail: "expire stale registrations: " + err.Error(),
			Fix: "mtix agent list --registrations"})
	}
	views, err := svc.ListRegistrations(ctx)
	if err != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "agents.stale", Detail: "list registrations: " + err.Error()})
	}
	report = appendDoctorCheck(report, staleRegistrationCheck(views, expired))
	return appendDoctorCheck(report, mcpOnlyRegistrationCheck(views))
}

func staleRegistrationCheck(views []service.RegistrationView, expired []sqlite.Registration) DoctorCheck {
	check := DoctorCheck{Name: "agents.stale", Pass: true, Detail: "no dead registrations"}
	var dead []string
	for _, v := range views {
		if v.Liveness == "dead" {
			dead = append(dead, v.AgentID)
		}
	}
	sort.Strings(dead)
	var parts []string
	if len(expired) > 0 {
		names := make([]string, 0, len(expired))
		for _, r := range expired {
			names = append(names, r.AgentID)
		}
		parts = append(parts, fmt.Sprintf("expired %d registration(s) unseen for 24 hours with a dead session: %s", len(names), strings.Join(names, ", ")))
	}
	if len(dead) > 0 {
		check.Warn = true
		parts = append(parts, fmt.Sprintf("dead session(s) still registered (replaced on the next register): %s", strings.Join(dead, ", ")))
		check.Fix = "mtix agent unregister " + dead[0] + " --force"
	}
	if len(parts) > 0 {
		check.Detail = strings.Join(parts, "; ")
	}
	return check
}

func mcpOnlyRegistrationCheck(views []service.RegistrationView) DoctorCheck {
	check := DoctorCheck{Name: "agents.mcp-only", Pass: true, Detail: "no MCP-only registrations"}
	var ids []string
	for _, v := range views {
		if v.Origin == "mcp" {
			ids = append(ids, v.AgentID)
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		check.Warn = true
		check.Detail = "registered through MCP only, so they have no live wake route: " + strings.Join(ids, ", ")
		check.Fix = "register from the session shell: mtix agent register " + ids[0]
	}
	return check
}
