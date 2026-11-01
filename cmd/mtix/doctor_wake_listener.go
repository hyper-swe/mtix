// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"fmt"
	"time"

	"github.com/hyper-swe/mtix/internal/service"
)

// registerWakeListenerDoctor registers the wake.listener rows (MTIX-112.40.6). Rows for
// routes whose adapter is not built are registered too and report data.reachable=false.
func registerWakeListenerDoctor(svc *service.DoctorService) error {
	if app.store == nil {
		return nil
	}
	checks := service.WakeListenerDoctorChecks(service.WakeListenerDoctorDeps{
		Agents:     registeredWakeAgents,
		Dispatcher: dispatcherAlive,
		Status:     newCLIWakeStatusService(),
		Clock:      time.Now,
	})
	// MTIX-112.40.4: hook failures never break a session, so doctor is where they show.
	checks = append(checks, service.ClaudeHookDoctorCheck(claudeHookLog()))
	for _, check := range checks {
		if err := svc.Register(check); err != nil {
			return fmt.Errorf("register %s: %w", check.ID, err)
		}
	}
	return nil
}
