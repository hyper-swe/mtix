// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"

	"github.com/hyper-swe/mtix/internal/service"
)

// registerDetachedListenerCheck exposes the existing listener inspection only
// for configured relays (MTIX-276.1). No fixer is registered: even --fix must
// preserve processes and the waiter-owned leases.
func registerDetachedListenerCheck(svc *service.DoctorService, relay bool, factory func() (*listenerCtl, error)) error {
	if !relay {
		return nil
	}
	err := svc.Register(service.DoctorCheck{ID: "daemon.detached-listener", Run: func(context.Context) service.DoctorResult {
		ctl, err := factory()
		if err != nil {
			return detachedListenerError(err)
		}
		rep, err := ctl.inspect()
		if err != nil {
			return detachedListenerError(err)
		}
		return detachedListenerDoctorRow(rep)
	}})
	if err != nil {
		return fmt.Errorf("register detached listener check: %w", err)
	}
	return nil
}

// detachedListenerError keeps an unreadable state distinct from a stopped listener.
func detachedListenerError(err error) service.DoctorResult {
	return service.DoctorResult{Status: service.DoctorWarn,
		Detail: fmt.Sprintf("cannot inspect the detached listener: %v", err),
		Fix:    "mtix daemon status --listener"}
}

// detachedListenerDoctorRow reports the same process classification as status,
// with explicit verification for maintenance (MTIX-276.1).
func detachedListenerDoctorRow(rep listenerReport) service.DoctorResult {
	row := service.DoctorResult{Status: service.DoctorPass, Detail: rep.Detail,
		Data: map[string]any{"state": rep.State, "pid": rep.PID, "waiters": rep.Waiters}}
	switch rep.State {
	case listenerStateRunning:
		row.Status = service.DoctorInfo
		row.Detail = fmt.Sprintf("detached listener running (pid %d); before maintenance, end every mtix inbox --wait in this project, then run the fix; expect status exit 10 (lock free, nothing running), not exit 1 (error), 12 (non-listener daemon) or 8 (unverified holder). Stop reports but does not release waiter leases; they end with their waiters or expire, and live waiters can restart the listener", rep.PID)
		row.Fix = "mtix daemon stop --listener && mtix daemon status --listener"
	case listenerStateDaemon:
		row.Status = service.DoctorInfo
		row.Fix = "mtix daemon status --listener"
	case listenerStateUnverified:
		row.Status = service.DoctorWarn
		row.Fix = "mtix daemon status --listener"
	}
	return row
}
