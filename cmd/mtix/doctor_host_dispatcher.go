// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/hyper-swe/mtix/internal/oplocal"
	"github.com/hyper-swe/mtix/internal/service"
)

func registerHostDispatcherDoctor(svc *service.DoctorService) error {
	state, err := operatorState()
	if err != nil {
		return nil
	} // attach.state already reports unusable placement
	installed := hostDispatcherInstalled(state)
	registry, err := state.DispatchBoards(context.Background())
	if err != nil {
		status := service.DoctorWarn
		if installed {
			status = service.DoctorFail
		}
		// Unused experimental state must not make the baseline doctor command fail.
		return svc.Register(service.DoctorCheck{ID: "dispatcher.host", Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: status, Detail: "host board registry cannot be read: " + err.Error(), Fix: operatorStateFix, Data: map[string]any{"installed": installed, "experimental": true}}
		}})
	}
	if len(registry.Listed) == 0 && !installed {
		return nil
	}
	host := service.DoctorCheck{ID: "dispatcher.host", Run: func(context.Context) service.DoctorResult {
		detail := "experimental host dispatcher is disabled by default; no host service is installed"
		fix := ""
		if installed {
			detail = "experimental per-user dispatcher service is installed; live routes depend on harness integration"
			fix = "mtix dispatcher status"
		}
		return service.DoctorResult{Status: service.DoctorInfo, Detail: detail, Fix: fix, Data: map[string]any{"installed": installed, "experimental": true, "listed": len(registry.Listed)}}
	}}
	if err := svc.Register(host); err != nil {
		return err
	}
	if app.store == nil || app.mtixDir == "" {
		return nil
	}
	return registerHostEnrollmentDoctor(svc, registry)
}

func registerHostEnrollmentDoctor(svc *service.DoctorService, registry oplocal.DispatchBoards) error {
	root, err := filepath.EvalSymlinks(filepath.Dir(app.mtixDir))
	if err != nil {
		return err
	}
	instance, err := app.store.StoreInstanceID(context.Background())
	if err != nil {
		return err
	}
	enrolled := false
	for _, row := range registry.Enrolled {
		if row.Path == root && row.StoreInstanceID == instance {
			enrolled = true
		}
	}
	check := service.DoctorCheck{ID: "board.enrolled", Run: func(context.Context) service.DoctorResult {
		detail := "checkout is not enrolled; enrollment is a service API until setup integration ships; existing project daemons remain available"
		status := service.DoctorInfo
		if enrolled {
			detail = "checkout is explicitly enrolled for host pull and user hooks"
			status = service.DoctorPass
		}
		return service.DoctorResult{Status: status, Detail: detail, Data: map[string]any{"enrolled": enrolled, "enrollment_history": registry.History}}
	}}
	if registerErr := svc.Register(check); registerErr != nil {
		return registerErr
	}
	if enrolled {
		return nil
	}
	return registerUnenrolledHostDoctor(svc, root)
}

func registerUnenrolledHostDoctor(svc *service.DoctorService, root string) error {
	connected, err := service.NewHubMarkService(app.store).Inspect(context.Background(), lenientHubEvidence())
	if err != nil {
		return err
	}
	info, statErr := os.Lstat(filepath.Join(root, ".git"))
	if connected.Connected && statErr == nil && info.Mode().IsRegular() && oplocal.ValidateLaneLocation(oplocal.LaneLocation{Harness: "dispatcher", Root: root, Path: root}) == nil {
		if err := svc.Register(service.DoctorCheck{ID: "board.worktree-unenrolled", Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorWarn, Detail: "hub worktree is not enrolled on this host; no host pull or live wake runs for it; enrollment awaits setup integration", Data: map[string]any{"enrolled": false}}
		}}); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(filepath.Join(app.mtixDir, "sync.daemon.pid")); err == nil {
		return svc.Register(service.DoctorCheck{ID: "dispatcher.legacy-service", Run: func(context.Context) service.DoctorResult {
			return service.DoctorResult{Status: service.DoctorInfo, Detail: "per-project daemon ownership is retained until explicit setup migration"}
		}})
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect legacy daemon ownership: %w", err)
	}
	return nil
}

type hostServiceRecord struct {
	Installed bool `json:"installed"`
}

func hostDispatcherInstalled(state *oplocal.State) bool {
	var record hostServiceRecord
	reports, err := state.ReadJSON("dispatcher-service", &record)
	return err == nil && len(reports) == 0 && record.Installed
}
