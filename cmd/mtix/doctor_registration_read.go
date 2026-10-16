// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/procinfo"
	"github.com/hyper-swe/mtix/internal/service"
)

func appendReadOnlyRegistrationChecks(ctx context.Context, report DoctorReport) DoctorReport {
	if app.store == nil {
		return report
	}
	host, err := existingRegistrationDoctorHost()
	if err != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "agents.stale", Pass: true, Warn: true, Detail: "registrations not checked: " + err.Error()})
	}
	if host.HostID == "" {
		return appendDoctorCheck(report, DoctorCheck{Name: "agents.stale", Pass: true, Warn: true, Detail: "registrations not checked: no existing host identity"})
	}
	svc := service.NewRegistrationService(service.RegistrationDeps{Store: app.store, Host: host, Liveness: service.NewProcessLiveness(procinfo.StartTime), Logger: app.logger})
	views, err := svc.ListRegistrations(ctx)
	if err != nil {
		return appendDoctorCheck(report, DoctorCheck{Name: "agents.stale", Detail: "list registrations: " + err.Error(), Fix: "mtix agent list --registrations"})
	}
	report = appendDoctorCheck(report, staleRegistrationCheck(views, nil))
	return appendDoctorCheck(report, mcpOnlyRegistrationCheck(views))
}

func existingRegistrationDoctorHost() (service.RegistrationHost, error) {
	env, err := oplocal.FromProcess()
	if err != nil {
		return service.RegistrationHost{}, fmt.Errorf("session identity: %w", err)
	}
	host, err := oplocal.New(env).ReadHostID()
	if err != nil {
		return service.RegistrationHost{}, fmt.Errorf("session host identity: %w", err)
	}
	if host == "" {
		return service.RegistrationHost{}, nil
	}
	uid, err := currentUserID()
	if err != nil {
		return service.RegistrationHost{}, fmt.Errorf("session user identity: %w", err)
	}
	return service.RegistrationHost{HostID: host, UID: uid, Version: version, Protocol: attachProtocol}, nil
}
