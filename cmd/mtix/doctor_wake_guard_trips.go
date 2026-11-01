// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

// registerWakeGuardTripsDoctor adds the read-only wake.guard-trips row (text and --json).
func registerWakeGuardTripsDoctor(svc *service.DoctorService) error {
	err := svc.Register(service.DoctorCheck{ID: "wake.guard-trips", Run: func(ctx context.Context) service.DoctorResult {
		env, envErr := oplocal.FromProcess()
		if envErr != nil {
			return service.DoctorResult{Status: service.DoctorWarn, Detail: fmt.Sprintf("Operator environment is unavailable: %s", envErr),
				Fix: "Restore the operator configuration path and run mtix doctor --json."}
		}
		return service.WakeGuardTripsDoctorCheck(scrubbedTrips{service.NewAlerts(oplocal.New(env), time.Now)}).Run(ctx)
	}})
	if err != nil {
		return fmt.Errorf("register wake guard-trips diagnostic: %w", err)
	}
	return nil
}

// scrubbedTrips redacts known secrets from alert text before the doctor row shows it.
type scrubbedTrips struct{ inner service.WakeGuardTripsReader }

func (s scrubbedTrips) Recent(ctx context.Context, limit int) ([]oplocal.Alert, error) {
	rows, err := s.inner.Recent(ctx, limit)
	if err != nil {
		return nil, err //nolint:wrapcheck // adapter forwards the reader's already-wrapped error unchanged
	}
	return scrubRecentAlerts(rows), nil
}
