// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"

	"github.com/hyper-swe/mtix/internal/service"
)

func registerConfiguredWakeLedgerDoctor(svc *service.DoctorService) error {
	if app.store == nil {
		return nil
	}
	if err := svc.Register(service.WakeLedgerDoctorCheck(app.store)); err != nil {
		return fmt.Errorf("register wake ledger diagnostic: %w", err)
	}
	return nil
}
