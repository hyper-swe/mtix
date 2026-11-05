// Copyright 2025-2026 HyperSWE
// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

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
	if err := registerHostRoutingDoctor(svc, installed); err != nil {
		return err
	}
	if len(registry.Listed) == 0 && !installed {
		return nil
	}
	if err := registerHostDoctorSummary(svc, registry, installed); err != nil {
		return err
	}
	if app.store == nil || app.mtixDir == "" {
		return nil
	}
	if err := registerHostEnrollmentDoctor(svc, registry); err != nil {
		return registerHostMetadataError(svc, "board.dispatcher-metadata", installed, err)
	}
	return nil
}

func registerHostDoctorSummary(svc *service.DoctorService, registry oplocal.DispatchBoards, installed bool) error {
	host := service.DoctorCheck{ID: "dispatcher.host", Run: func(context.Context) service.DoctorResult {
		detail := "experimental host dispatcher is disabled by default; no host service is installed"
		fix := ""
		if installed {
			detail = "experimental per-user dispatcher service is installed; live routes depend on harness integration"
			fix = "mtix dispatcher status"
		}
		return service.DoctorResult{Status: service.DoctorInfo, Detail: detail, Fix: fix, Data: map[string]any{"installed": installed, "experimental": true, "listed": len(registry.Listed)}}
	}}
	return svc.Register(host)
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
		return registerLegacyDoctorForHost(svc)
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
	return registerLegacyDoctorForHost(svc)
}

// registerLegacyDoctorForHost wires dispatcher.legacy-service for enrolled and unenrolled boards.
func registerLegacyDoctorForHost(svc *service.DoctorService) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve home for legacy service check: %w", err)
	}
	return registerLegacyServiceDoctor(svc, app.mtixDir, runtime.GOOS, home)
}

type hostServiceRecord struct {
	Installed bool `json:"installed"`
}

func hostDispatcherInstalled(state *oplocal.State) bool {
	var record hostServiceRecord
	reports, err := state.ReadJSON("dispatcher-service", &record)
	return err == nil && len(reports) == 0 && record.Installed
}

func registerHostMetadataError(svc *service.DoctorService, id string, installed bool, problem error) error {
	status := service.DoctorWarn
	if installed {
		status = service.DoctorFail
	}
	return svc.Register(service.DoctorCheck{ID: id, Run: func(context.Context) service.DoctorResult {
		return service.DoctorResult{Status: status, Detail: "cannot inspect checkout dispatcher metadata: " + problem.Error(), Fix: "Resolve the reported checkout metadata error, then rerun mtix agent register <agent-id> and mtix doctor", Data: map[string]any{"experimental": true}}
	}})
}

func registerHostRoutingDoctor(svc *service.DoctorService, installed bool) error {
	routing, err := currentRegistrationRouting()
	if err == nil && routing != nil {
		err = oplocal.ValidateLaneLocation(oplocal.LaneLocation{Harness: "dispatcher", Root: routing.Board.Path, Path: routing.Board.Path, BoardKey: routing.Board.Key})
	}
	if err != nil {
		return registerHostMetadataError(svc, "board.routing", installed, err)
	}
	return nil
}
