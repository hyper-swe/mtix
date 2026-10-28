// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// registerConfiguredWakeBudgetDoctor registers the wake accounting diagnostics: the
// operator-local wake.guard-trips row always, and budget.rewind on an opened board.
func registerConfiguredWakeBudgetDoctor(svc *service.DoctorService) error {
	if err := registerWakeGuardTripsDoctor(svc); err != nil {
		return err
	}
	if app.store == nil {
		return nil
	}
	store := app.store
	err := svc.Register(service.DoctorCheck{ID: "budget.rewind", Run: func(ctx context.Context) service.DoctorResult {
		return configuredWakeBudgetRow(ctx, store)
	}})
	if err != nil {
		return fmt.Errorf("register wake budget diagnostic: %w", err)
	}
	return nil
}

func configuredWakeBudgetRow(ctx context.Context, store service.WakeBudgetReader) service.DoctorResult {
	env, err := oplocal.FromProcess()
	if err != nil {
		return service.DoctorResult{Status: service.DoctorFail, Detail: fmt.Sprintf("Wake accounting environment is unavailable: %s", err),
			Fix: "Restore the operator configuration path and run mtix doctor --json."}
	}
	return service.WakeBudgetDoctorCheck(store, oplocal.New(env)).Run(ctx)
}

func appendWakeBudgetRewindCheck(ctx context.Context, report DoctorReport, store service.WakeBudgetReader) DoctorReport {
	row := configuredWakeBudgetRow(ctx, store)
	return appendDoctorCheck(report, DoctorCheck{Name: "budget.rewind", Pass: row.Status != service.DoctorFail,
		Warn: row.Status == service.DoctorWarn, Detail: row.Detail, Fix: row.Fix})
}
